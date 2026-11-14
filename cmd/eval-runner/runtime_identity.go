package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"strings"

	"github.com/markhuangai/dense-mem/internal/evalharness"
)

type runtimeContainer struct {
	Config struct {
		Image  string
		Labels map[string]string
	}
	State           struct{ Running bool }
	NetworkSettings struct {
		Ports    map[string][]struct{ HostIP, HostPort string }
		Networks map[string]json.RawMessage
	}
}

func verifyRuntime(ctx context.Context, manifest evalharness.Manifest) error {
	prefix := "densemem-eval-" + manifest.RunID
	containers := make([]runtimeContainer, 2)
	for index, kind := range []string{"server", "postgres"} {
		body, err := exec.CommandContext(ctx, "docker", "container", "inspect", "--format", "{{json .}}", prefix+"-"+kind).Output()
		if err != nil || json.Unmarshal(body, &containers[index]) != nil {
			return errors.New("runner container identity cannot be verified")
		}
	}
	body, err := exec.CommandContext(ctx, "docker", "network", "inspect", "--format", "{{json .Labels}}", prefix+"-network").Output()
	var labels map[string]string
	if err != nil || json.Unmarshal(body, &labels) != nil || labels["io.dense-mem.eval.run"] != manifest.RunID {
		return errors.New("runner network ownership cannot be verified")
	}
	return validateRuntime(manifest, os.Getenv, containers[0], containers[1])
}

func validateRuntime(manifest evalharness.Manifest, setting func(string) string, server, postgres runtimeContainer) error {
	if manifest.RunID != manifest.WorkflowRunID+"-"+manifest.Dataset || setting("EVAL_SERVER_CONTAINER") != "densemem-eval-"+manifest.RunID+"-server" {
		return errors.New("runtime does not belong to this dataset job")
	}
	for _, container := range []runtimeContainer{server, postgres} {
		if !container.State.Running || container.Config.Labels["io.dense-mem.eval.run"] != manifest.RunID || container.NetworkSettings.Networks["densemem-eval-"+manifest.RunID+"-network"] == nil {
			return errors.New("runtime container ownership differs from this dataset job")
		}
	}
	if server.Config.Image != manifest.Image || postgres.Config.Image != "pgvector/pgvector:0.8.2-pg18-trixie" {
		return errors.New("runtime images differ from the workflow manifest")
	}
	port := func(container runtimeContainer, internal string) string {
		bindings := container.NetworkSettings.Ports[internal+"/tcp"]
		if len(bindings) != 1 || bindings[0].HostIP != "127.0.0.1" || bindings[0].HostPort == "" {
			return ""
		}
		return bindings[0].HostPort
	}
	for _, endpoint := range []struct{ Name, Port string }{{"EVAL_MCP_URL", "8080"}, {"EVAL_CONTROL_URL", "8090"}} {
		bound := port(server, endpoint.Port)
		if bound == "" || setting(endpoint.Name) != "http://127.0.0.1:"+bound {
			return errors.New("runtime endpoint differs from its owned loopback port")
		}
	}
	bound := port(postgres, "5432")
	database, err := url.Parse(setting("EVAL_DATABASE_URL"))
	if err != nil || bound == "" || database.Scheme != "postgres" || database.Host != "127.0.0.1:"+bound || database.Path != "/densemem" || database.RawQuery != "sslmode=disable" || database.Fragment != "" || database.User == nil || database.User.Username() != "densemem" {
		return errors.New("database endpoint differs from the owned runner database")
	}
	password, _ := database.User.Password()
	if password == "" || password != setting("EVAL_DATABASE_PASSWORD") || strings.ContainsAny(password, "\r\n") {
		return errors.New("database credential differs from this dataset job")
	}
	return nil
}
