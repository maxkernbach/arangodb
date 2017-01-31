package service

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	docker "github.com/fsouza/go-dockerclient"
	"github.com/juju/errgo"
	logging "github.com/op/go-logging"
)

const (
	stopContainerTimeout = 60 // Seconds before a container is killed (after graceful stop)
)

// NewDockerRunner creates a runner that starts processes on the local OS.
func NewDockerRunner(log *logging.Logger, endpoint, image, user, volumesFrom string, gcDelay time.Duration) (Runner, error) {
	client, err := docker.NewClient(endpoint)
	if err != nil {
		return nil, maskAny(err)
	}
	return &dockerRunner{
		log:          log,
		client:       client,
		image:        image,
		user:         user,
		volumesFrom:  volumesFrom,
		containerIDs: make(map[string]time.Time),
		gcDelay:      gcDelay,
	}, nil
}

// dockerRunner implements a Runner that starts processes in a docker container.
type dockerRunner struct {
	log          *logging.Logger
	client       *docker.Client
	image        string
	user         string
	volumesFrom  string
	mutex        sync.Mutex
	containerIDs map[string]time.Time
	gcOnce       sync.Once
	gcDelay      time.Duration
}

type dockerContainer struct {
	client    *docker.Client
	container *docker.Container
}

func (r *dockerRunner) GetContainerDir(hostDir string) string {
	if r.volumesFrom != "" {
		return hostDir
	}
	return "/data"
}

func (r *dockerRunner) Start(command string, args []string, volumes []Volume, ports []int, containerName string) (Process, error) {
	// Start gc (once)
	r.gcOnce.Do(func() { go r.gc() })

	// Pull docker image
	repo, tag := docker.ParseRepositoryTag(r.image)
	r.log.Debugf("Pulling image %s:%s", repo, tag)
	if err := r.client.PullImage(docker.PullImageOptions{
		Repository: repo,
		Tag:        tag,
	}, docker.AuthConfiguration{}); err != nil {
		return nil, maskAny(err)
	}

	containerName = strings.Replace(containerName, ":", "", -1)
	opts := docker.CreateContainerOptions{
		Name: containerName,
		Config: &docker.Config{
			Image:        r.image,
			Entrypoint:   []string{command},
			Cmd:          args,
			Tty:          true,
			User:         r.user,
			ExposedPorts: make(map[docker.Port]struct{}),
		},
		HostConfig: &docker.HostConfig{
			PortBindings:    make(map[docker.Port][]docker.PortBinding),
			PublishAllPorts: true,
			AutoRemove:      false,
		},
	}
	if r.volumesFrom != "" {
		opts.HostConfig.VolumesFrom = []string{r.volumesFrom}
	} else {
		for _, v := range volumes {
			bind := fmt.Sprintf("%s:%s", v.HostPath, v.ContainerPath)
			if v.ReadOnly {
				bind = bind + ":ro"
			}
			opts.HostConfig.Binds = append(opts.HostConfig.Binds, bind)
		}
	}
	for _, p := range ports {
		dockerPort := docker.Port(fmt.Sprintf("%d/tcp", p))
		opts.Config.ExposedPorts[dockerPort] = struct{}{}
		opts.HostConfig.PortBindings[dockerPort] = []docker.PortBinding{
			docker.PortBinding{
				HostIP:   "0.0.0.0",
				HostPort: strconv.Itoa(p),
			},
		}
	}
	r.log.Debugf("Creating container %s", containerName)
	c, err := r.client.CreateContainer(opts)
	if err != nil {
		return nil, maskAny(err)
	}
	r.recordContainerID(c.ID) // Record ID so we can clean it up later
	r.log.Debugf("Starting container %s", containerName)
	if err := r.client.StartContainer(c.ID, opts.HostConfig); err != nil {
		return nil, maskAny(err)
	}
	r.log.Debugf("Started container %s", containerName)
	return &dockerContainer{
		client:    r.client,
		container: c,
	}, nil
}

func (r *dockerRunner) CreateStartArangodbCommand(index int, masterIP string, masterPort string) string {
	addr := masterIP
	hostPort := 4000 + (portOffsetIncrement * (index - 1))
	if masterPort != "" {
		addr = addr + ":" + masterPort
		masterPortI, _ := strconv.Atoi(masterPort)
		hostPort = masterPortI + (portOffsetIncrement * (index - 1))
	}
	lines := []string{
		fmt.Sprintf("docker volume create arangodb%d &&", index),
		fmt.Sprintf("docker run -it --name=adb%d --rm -p %d:4000 -v arangodb%d:/data -v /var/run/docker.sock:/var/run/docker.sock arangodb/arangodb-starter", index, hostPort, index),
		fmt.Sprintf("--dockerContainer=adb%d --ownAddress=%s --join=%s", index, masterIP, addr),
	}
	return strings.Join(lines, " \\\n    ")
}

// Cleanup after all processes are dead and have been cleaned themselves
func (r *dockerRunner) Cleanup() error {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	for id := range r.containerIDs {
		r.log.Infof("Removing container %s", id)
		if err := r.client.RemoveContainer(docker.RemoveContainerOptions{
			ID:    id,
			Force: true,
		}); err != nil && !isNoSuchContainer(err) {
			r.log.Warningf("Failed to remove container %s: %#v", id, err)
		}
	}
	r.containerIDs = nil

	return nil
}

// recordContainerID records an ID of a created container
func (r *dockerRunner) recordContainerID(id string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.containerIDs[id] = time.Now()
}

// unrecordContainerID removes an ID from the list of created containers
func (r *dockerRunner) unrecordContainerID(id string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	delete(r.containerIDs, id)
}

// gc performs continues garbage collection of stopped old containers
func (r *dockerRunner) gc() {
	canGC := func(c *docker.Container) bool {
		gcBoundary := time.Now().UTC().Add(-r.gcDelay)
		switch c.State.StateString() {
		case "dead", "exited":
			if c.State.FinishedAt.Before(gcBoundary) {
				// Dead or exited long enough
				return true
			}
		case "created":
			if c.Created.Before(gcBoundary) {
				// Created but not running long enough
				return true
			}
		}
		return false
	}
	for {
		ids := r.gatherCollectableContainerIDs()
		for _, id := range ids {
			c, err := r.client.InspectContainer(id)
			if err != nil {
				if isNoSuchContainer(err) {
					// container no longer exists
					r.unrecordContainerID(id)
				} else {
					r.log.Warningf("Failed to inspect container %s: %#v", id, err)
				}
			} else if canGC(c) {
				// Container is dead for more than 10 minutes, gc it.
				r.log.Infof("Removing old container %s", id)
				if err := r.client.RemoveContainer(docker.RemoveContainerOptions{
					ID: id,
				}); err != nil {
					r.log.Warningf("Failed to remove container %s: %#v", id, err)
				} else {
					// Remove succeeded
					r.unrecordContainerID(id)
				}
			}
		}
		time.Sleep(time.Minute)
	}
}

// gatherCollectableContainerIDs returns all container ID's that are old enough to be consider for garbage collection.
func (r *dockerRunner) gatherCollectableContainerIDs() []string {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	var result []string
	gcBoundary := time.Now().Add(-r.gcDelay)
	for id, ts := range r.containerIDs {
		if ts.Before(gcBoundary) {
			result = append(result, id)
		}
	}
	return result
}

// ProcessID returns the pid of the process (if not running in docker)
func (p *dockerContainer) ProcessID() int {
	return 0
}

// ContainerID returns the ID of the docker container that runs the process.
func (p *dockerContainer) ContainerID() string {
	return p.container.ID
}

func (p *dockerContainer) Wait() {
	p.client.WaitContainer(p.container.ID)
}

func (p *dockerContainer) Terminate() error {
	if err := p.client.StopContainer(p.container.ID, stopContainerTimeout); err != nil {
		return maskAny(err)
	}
	return nil
}

func (p *dockerContainer) Kill() error {
	if err := p.client.KillContainer(docker.KillContainerOptions{
		ID: p.container.ID,
	}); err != nil {
		return maskAny(err)
	}
	return nil
}

func (p *dockerContainer) Cleanup() error {
	opts := docker.RemoveContainerOptions{
		ID:    p.container.ID,
		Force: true,
	}
	if err := p.client.RemoveContainer(opts); err != nil {
		return maskAny(err)
	}
	return nil
}

// isNoSuchContainer returns true if the given error is (or is caused by) a NoSuchContainer error.
func isNoSuchContainer(err error) bool {
	if _, ok := err.(*docker.NoSuchContainer); ok {
		return true
	}
	if _, ok := errgo.Cause(err).(*docker.NoSuchContainer); ok {
		return true
	}
	return false
}
