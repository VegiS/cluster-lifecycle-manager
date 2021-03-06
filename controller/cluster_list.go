package controller

import (
	"sync"
	"time"

	"github.com/zalando-incubator/cluster-lifecycle-manager/config"
	log "github.com/sirupsen/logrus"

	"github.com/zalando-incubator/cluster-lifecycle-manager/api"
)

const (
	updatePriorityNormal = iota
	updatePriorityDecommissionRequested
	updatePriorityAlreadyUpdating
)

type clusterInfo struct {
	lastProcessed time.Time
	processing    bool
	cluster       *api.Cluster
}

// ClusterList maintains the state of all active clusters
type ClusterList struct {
	sync.Mutex
	accountFilter config.IncludeExcludeFilter
	clusters      map[string]*clusterInfo
}

func NewClusterList(accountFilter config.IncludeExcludeFilter) *ClusterList {
	return &ClusterList{
		accountFilter: accountFilter,
		clusters:      make(map[string]*clusterInfo),
	}
}

// UpdateAvailable adds new clusters to the list, updates the cluster data for existing ones and removes clusters
// that are no longer active
func (clusterList *ClusterList) UpdateAvailable(availableClusters []*api.Cluster) {
	clusterList.Lock()
	defer clusterList.Unlock()

	availableClusterIds := make(map[string]bool)

	for _, cluster := range availableClusters {
		if cluster.LifecycleStatus == statusDecommissioned {
			log.Debugf("Cluster decommissioned: %s", cluster.ID)
			continue
		}

		if !clusterList.accountFilter.Allowed(cluster.InfrastructureAccount) {
			log.Infof("Skipping %s cluster, infrastructure account does not match provided filter.", cluster.ID)
			continue
		}

		availableClusterIds[cluster.ID] = true

		if existing, ok := clusterList.clusters[cluster.ID]; ok {
			if !existing.processing {
				existing.cluster = cluster
			}
		} else {
			clusterList.clusters[cluster.ID] = &clusterInfo{
				lastProcessed: time.Unix(0, 0),
				processing:    false,
				cluster:       cluster,
			}
		}
	}

	for id, cluster := range clusterList.clusters {
		// keep clusters that are still being updated to avoid race conditions
		// if they're deleted and then added again
		if cluster.processing {
			continue
		}

		if _, ok := availableClusterIds[id]; !ok {
			delete(clusterList.clusters, id)
		}
	}
}

// updatePriority returns the update priority of the clusters. Clusters with higher priority will always be selected
// for update before clusters with lower priority.
func updatePriority(cluster *api.Cluster) uint32 {
	if cluster.Status.NextVersion != "" && cluster.Status.NextVersion != cluster.Status.CurrentVersion {
		return updatePriorityAlreadyUpdating
	}
	if cluster.LifecycleStatus == statusDecommissionRequested {
		return updatePriorityDecommissionRequested
	}
	return updatePriorityNormal
}

// SelectNext returns the next cluster of update, if any, and marks it as being processed. A cluster with higher
// priority will be selected first, in case of ties it'll select a cluster that hasn't been updated for the longest
// time.
func (clusterList *ClusterList) SelectNext() *api.Cluster {
	clusterList.Lock()
	defer clusterList.Unlock()

	var nextCluster *clusterInfo
	var nextClusterPriority uint32

	for _, cluster := range clusterList.clusters {
		if cluster.processing {
			continue
		}

		if nextCluster == nil {
			nextCluster = cluster
			nextClusterPriority = updatePriority(cluster.cluster)
		} else {
			priority := updatePriority(cluster.cluster)

			if priority > nextClusterPriority || (priority == nextClusterPriority && cluster.lastProcessed.Before(nextCluster.lastProcessed)) {
				nextCluster = cluster
				nextClusterPriority = priority
			}
		}
	}

	if nextCluster == nil {
		return nil
	}

	nextCluster.processing = true
	return nextCluster.cluster
}

// ClusterProcessed marks a cluster as no longer being processed.
func (clusterList *ClusterList) ClusterProcessed(id string) {
	clusterList.Lock()
	defer clusterList.Unlock()

	if cluster, ok := clusterList.clusters[id]; ok {
		cluster.processing = false
		cluster.lastProcessed = time.Now()
	}
}
