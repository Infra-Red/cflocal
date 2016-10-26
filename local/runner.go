package local

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
	docker "github.com/docker/docker/client"

	"github.com/docker/go-connections/nat"
	"github.com/sclevine/cflocal/utils"
)

type Runner struct {
	Docker   *docker.Client
	Logs     io.Writer
	ExitChan <-chan struct{}
}

type RunConfig struct {
	Droplet      io.ReadCloser
	DropletSize  int64
	Launcher     io.ReadCloser
	LauncherSize int64
	Port         uint
	AppConfig    *AppConfig
}

func (r *Runner) Run(config *RunConfig, color Colorizer) (status int, err error) {
	name := config.AppConfig.Name
	vcapApp, err := json.Marshal(&vcapApplication{
		ApplicationID:      "01d31c12-d066-495e-aca2-8d3403165360",
		ApplicationName:    name,
		ApplicationURIs:    []string{"localhost"},
		ApplicationVersion: "2b860df9-a0a1-474c-b02f-5985f53ea0bb",
		Host:               "0.0.0.0",
		InstanceID:         "999db41a-508b-46eb-74d8-6f9c06c006da",
		InstanceIndex:      uintPtr(0),
		Limits:             map[string]uint{"fds": 16384, "mem": 512, "disk": 1024},
		Name:               name,
		Port:               uintPtr(8080),
		SpaceID:            "18300c1c-1aa4-4ae7-81e6-ae59c6cdbaf1",
		SpaceName:          "cflocal-space",
		URIs:               []string{"localhost"},
		Version:            "18300c1c-1aa4-4ae7-81e6-ae59c6cdbaf1",
	})
	if err != nil {
		return 0, err
	}
	env := map[string]string{
		"CF_INSTANCE_ADDR":  "0.0.0.0:8080",
		"CF_INSTANCE_GUID":  "999db41a-508b-46eb-74d8-6f9c06c006da",
		"CF_INSTANCE_INDEX": "0",
		"CF_INSTANCE_IP":    "0.0.0.0",
		"CF_INSTANCE_PORT":  "8080",
		"CF_INSTANCE_PORTS": `[{"external":8080,"internal":8080}]`,
		"HOME":              "/home/vcap",
		"INSTANCE_GUID":     "999db41a-508b-46eb-74d8-6f9c06c006da",
		"INSTANCE_INDEX":    "0",
		"LANG":              "en_US.UTF-8",
		"MEMORY_LIMIT":      "512m",
		"PATH":              "/usr/local/bin:/usr/bin:/bin",
		"PORT":              "8080",
		"TMPDIR":            "/home/vcap/tmp",
		"USER":              "vcap",
		"VCAP_APPLICATION":  string(vcapApp),
		"VCAP_SERVICES":     "{}",
	}
	untarDroplet := "tar -C /home/vcap -xzf /tmp/droplet"
	chownVCAP := "chown -R vcap:vcap /home/vcap"
	startCommand := "$(jq -r .start_command /home/vcap/staging_info.yml)"
	cont := utils.Container{Docker: r.Docker, Err: &err}
	id := cont.Create(name, config.Port, &container.Config{
		Hostname:     "cflocal",
		User:         "vcap",
		ExposedPorts: map[nat.Port]struct{}{"8080/tcp": struct{}{}},
		Env:          mapToEnv(mergeMaps(env, config.AppConfig.RunningEnv, config.AppConfig.Env)),
		Image:        "cloudfoundry/cflinuxfs2",
		WorkingDir:   "/home/vcap/app",
		Entrypoint: strslice.StrSlice{
			"/bin/bash", "-c",
			fmt.Sprintf(
				`%s && %s && /tmp/lifecycle/launcher /home/vcap/app "%s" ''`,
				untarDroplet, chownVCAP, startCommand,
			),
		},
	})
	if id == "" {
		return 0, err
	}
	defer cont.Remove(id)

	launcherTar, err := utils.TarFile("./lifecycle/launcher", config.Launcher, config.LauncherSize, 0755)
	if err != nil {
		return 0, err
	}
	if err := r.Docker.CopyToContainer(context.Background(), id, "/tmp", launcherTar, types.CopyToContainerOptions{}); err != nil {
		return 0, err
	}
	if err := config.Launcher.Close(); err != nil {
		return 0, err
	}

	dropletTar, err := utils.TarFile("./droplet", config.Droplet, config.DropletSize, 0755)
	if err != nil {
		return 0, err
	}
	if err := r.Docker.CopyToContainer(context.Background(), id, "/tmp", dropletTar, types.CopyToContainerOptions{}); err != nil {
		return 0, err
	}
	if err := config.Droplet.Close(); err != nil {
		return 0, err
	}

	if err := r.Docker.ContainerStart(context.Background(), id, types.ContainerStartOptions{}); err != nil {
		return 0, err
	}
	logs, err := r.Docker.ContainerLogs(context.Background(), id, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     true,
	})
	if err != nil {
		return 0, err
	}
	defer logs.Close()
	go utils.CopyStream(r.Logs, logs, color("[%s] ", name))

	go func() {
		<-r.ExitChan
		cont.Remove(id)
	}()
	status, err = r.Docker.ContainerWait(context.Background(), id)
	if err != nil {
		return 0, err
	}
	return status, nil
}

func mergeMaps(maps ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			merged[k] = v
		}
	}
	return merged
}

func mapToEnv(env map[string]string) []string {
	var out []string
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

func uintPtr(i uint) *uint {
	return &i
}
