package e2e

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	ott "github.com/opendevstack/ods-pipeline/pkg/odstasktest"
	ttr "github.com/opendevstack/ods-pipeline/pkg/tektontaskrun"
)

var (
	namespaceConfig *ttr.NamespaceConfig
	rootPath        = "../.."
	taskName        = "ods-pipeline-image-package"
)

func TestMain(m *testing.M) {
	os.Exit(testMain(m))
}

func testMain(m *testing.M) int {
	cc, err := ttr.StartKinDCluster(
		ttr.LoadImage(ttr.ImageBuildConfig{
			Dockerfile: "build/images/Dockerfile.package",
			ContextDir: rootPath,
		}),
	)
	if err != nil {
		log.Fatal("Could not start KinD cluster: ", err)
	}
	nc, cleanup, err := ttr.SetupTempNamespace(
		cc,
		ott.InstallODSPipeline(),
		ttr.InstallTaskFromPath(
			filepath.Join(rootPath, "build/tasks/package.yaml"),
			map[string]string{"PushRegistry": "ods-pipeline-registry.kind:5000"},
		),
	)
	if err != nil {
		log.Fatal("Could not setup temporary namespace: ", err)
	}
	defer cleanup()
	namespaceConfig = nc
	return m.Run()
}

func runTask(opts ...ttr.TaskRunOpt) error {
	return ttr.RunTask(append([]ttr.TaskRunOpt{
		ttr.InNamespace(namespaceConfig.Name),
		ttr.UsingTask(taskName),
	}, opts...)...)
}
