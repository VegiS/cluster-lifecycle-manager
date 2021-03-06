package channel

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/zalando-incubator/cluster-lifecycle-manager/pkg/util/command"
	log "github.com/sirupsen/logrus"
	"path/filepath"
)

// Git defines a channel source where the channels are stored in a git
// repository.
type Git struct {
	workdir           string
	repositoryURL     string
	repoName          string
	repoDir           string
	sshPrivateKeyFile string
	mutex             *sync.Mutex
}

// NewGit initializes a new git based ChannelSource.
func NewGit(workdir, repositoryURL, sshPrivateKeyFile string) (ConfigSource, error) {
	absWorkdir, err := filepath.Abs(workdir)
	if err != nil {
		return nil, err
	}

	// get repo name from repo URL.
	repoName, err := getRepoName(repositoryURL)
	if err != nil {
		return nil, err
	}

	return &Git{
		workdir:           absWorkdir,
		repoName:          repoName,
		repositoryURL:     repositoryURL,
		repoDir:           path.Join(absWorkdir, repoName),
		sshPrivateKeyFile: sshPrivateKeyFile,
		mutex:             &sync.Mutex{},
	}, nil
}

var repoNameRE = regexp.MustCompile(`/?([\w-]+)(.git)?$`)

// getRepoName parses the repository name given a repository URI.
func getRepoName(repoURI string) (string, error) {
	match := repoNameRE.FindStringSubmatch(repoURI)
	if len(match) != 3 {
		return "", fmt.Errorf("could not parse repository name from uri: %s", repoURI)
	}
	return match[1], nil
}

// Get checks out the specified channel from the git repo.
func (g *Git) Get(channel string) (*Config, error) {
	repoDir, err := g.localClone(channel)
	if err != nil {
		return nil, err
	}

	version, err := g.currentRevision(repoDir)
	if err != nil {
		return nil, err
	}

	return &Config{
		Version: version,
		Path:    repoDir,
	}, nil
}

// Delete deletes the underlying git repository checkout specified by the
// config Path.
func (g *Git) Delete(config *Config) error {
	return os.RemoveAll(config.Path)
}

func (g *Git) Update() error {
	g.mutex.Lock()
	defer g.mutex.Unlock()

	_, err := os.Stat(g.repoDir)
	if err != nil {
		if !os.IsNotExist(err) {
			return err
		}

		err = g.cmd("clone", "--mirror", g.repositoryURL, g.repoDir)
		if err != nil {
			return err
		}
	}

	err = g.cmd("--git-dir", g.repoDir, "remote", "update", "--prune")
	if err != nil {
		return err
	}

	return nil
}

// localClone duplicates a repo by cloning to temp location with unix time
// suffix this will be the path that is exposed through the Config. This
// makes sure that each caller (possibly running concurrently) get it's
// own version of the checkout, such that they can run concurrently
// without data races.
func (g *Git) localClone(channel string) (string, error) {
	repoDir := path.Join(g.workdir, fmt.Sprintf("%s_%s_%d", g.repoName, channel, time.Now().UTC().UnixNano()))

	srcRepoUrl := fmt.Sprintf("file://%s", g.repoDir)
	err := g.cmd("clone", srcRepoUrl, repoDir)
	if err != nil {
		return "", err
	}

	err = g.cmd("-C", repoDir, "checkout", channel)
	if err != nil {
		return "", err
	}

	return repoDir, nil
}

// currentRevision returns the current revision of the repoDir.
func (g *Git) currentRevision(repoDir string) (string, error) {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", "HEAD")
	d, err := cmd.Output()
	if err != nil {
		return "", err
	}

	return strings.TrimSpace(string(d)), err
}

// cmd executes a git command with the correct environment set.
func (g *Git) cmd(args ...string) error {
	cmd := exec.Command("git", args...)
	// set GIT_SSH_COMMAND with private-key file when pulling over ssh.
	if g.sshPrivateKeyFile != "" {
		cmd.Env = []string{fmt.Sprintf("GIT_SSH_COMMAND=ssh -i %s -o 'StrictHostKeyChecking no'", g.sshPrivateKeyFile)}
	}

	return command.Run(log.StandardLogger(), cmd)
}
