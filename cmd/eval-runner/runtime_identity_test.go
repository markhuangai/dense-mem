package main

import (
	"encoding/json"
	"testing"

	"github.com/markhuangai/dense-mem/internal/evalharness"
	"github.com/stretchr/testify/require"
)

func TestRuntimeEndpointsMustBelongToThisJobsProductionContainers(t *testing.T) {
	manifest := evalharness.Manifest{RunID: "42-hotpotqa", WorkflowRunID: "42", Dataset: "hotpotqa", Image: "pinned-production-image"}
	decode := func(image string) runtimeContainer {
		var container runtimeContainer
		require.NoError(t, json.Unmarshal([]byte(`{"Config":{"Image":"`+image+`","Labels":{"io.dense-mem.eval.run":"42-hotpotqa"}},"State":{"Running":true},"NetworkSettings":{"Ports":{"8080/tcp":[{"HostIP":"127.0.0.1","HostPort":"10080"}],"8090/tcp":[{"HostIP":"127.0.0.1","HostPort":"10090"}],"5432/tcp":[{"HostIP":"127.0.0.1","HostPort":"15432"}]},"Networks":{"densemem-eval-42-hotpotqa-network":{}}}}`), &container))
		return container
	}
	server, postgres := decode(manifest.Image), decode("pgvector/pgvector:0.8.2-pg18-trixie")
	settings := map[string]string{"EVAL_SERVER_CONTAINER": "densemem-eval-42-hotpotqa-server", "EVAL_MCP_URL": "http://127.0.0.1:10080", "EVAL_CONTROL_URL": "http://127.0.0.1:10090", "EVAL_DATABASE_URL": "postgres://densemem:generated@127.0.0.1:15432/densemem?sslmode=disable", "EVAL_DATABASE_PASSWORD": "generated"}
	get := func(key string) string { return settings[key] }
	require.NoError(t, validateRuntime(manifest, get, server, postgres))
	for _, key := range []string{"EVAL_SERVER_CONTAINER", "EVAL_MCP_URL", "EVAL_CONTROL_URL", "EVAL_DATABASE_URL", "EVAL_DATABASE_PASSWORD"} {
		t.Run(key, func(t *testing.T) {
			original := settings[key]
			settings[key] = "arbitrary-prepared-runtime"
			require.Error(t, validateRuntime(manifest, get, server, postgres))
			settings[key] = original
		})
	}
	server.Config.Labels["io.dense-mem.eval.run"] = "another-job"
	require.ErrorContains(t, validateRuntime(manifest, get, server, postgres), "ownership")
	server = decode("wrong-image")
	require.ErrorContains(t, validateRuntime(manifest, get, server, postgres), "images")
	server = decode(manifest.Image)
	server.NetworkSettings.Ports["8080/tcp"][0].HostIP = "0.0.0.0"
	require.ErrorContains(t, validateRuntime(manifest, get, server, postgres), "loopback")
}
