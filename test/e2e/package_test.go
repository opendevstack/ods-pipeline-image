package e2e

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/opendevstack/ods-pipeline/pkg/artifact"
	"github.com/opendevstack/ods-pipeline/pkg/logging"
	"github.com/opendevstack/ods-pipeline/pkg/nexus"
	ott "github.com/opendevstack/ods-pipeline/pkg/odstasktest"
	"github.com/opendevstack/ods-pipeline/pkg/pipelinectxt"
	ttr "github.com/opendevstack/ods-pipeline/pkg/tektontaskrun"
	tekton "github.com/tektoncd/pipeline/pkg/apis/pipeline/v1"
	"golang.org/x/exp/slices"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/homedir"
)

const cosignKeySecretName = "cosign-example-key"

func TestPackageImageTask(t *testing.T) {
	if err := runTask(
		ott.WithGitSourceWorkspace(t, "../testdata/workspaces/hello-world-app", namespaceConfig.Name),
		ttr.WithStringParams(map[string]string{
			"docker-dir": "docker",
			"cosign-key": fmt.Sprintf("k8s://%s/%s", namespaceConfig.Name, cosignKeySecretName),
		}),
		generateCosignKey(),
		ttr.AfterRun(func(config *ttr.TaskRunConfig, run *tekton.TaskRun, logs bytes.Buffer) {
			wsDir, ctxt := ott.GetSourceWorkspaceContext(t, config)
			checkResultingFiles(t, ctxt, wsDir)
			checkResultingImageHelloWorld(t, ctxt, wsDir)

			var resultImageDigest string
			var resultImageRef string
			results := run.Status.Results
			for _, v := range results {
				if v.Name == "image-ref" {
					resultImageRef = v.Value.StringVal
				} else if v.Name == "image-digest" {
					resultImageDigest = v.Value.StringVal
				}
			}
			if resultImageDigest == "" {
				t.Fatal("want result 'image-digest' to be set but it was empty")
			}
			wantImageRef := fmt.Sprintf("ods-pipeline-registry.kind:5000/%s/%s@%s", namespaceConfig.Name, filepath.Base(wsDir), resultImageDigest)
			if resultImageRef != wantImageRef {
				t.Fatalf("want image ref %q, got %q", wantImageRef, resultImageRef)
			}

			// check signature + SBOM attestation
			imageRef := fmt.Sprintf("localhost:5000/%s/%s@%s", namespaceConfig.Name, filepath.Base(wsDir), resultImageDigest)
			cmd := exec.Command("cosign", "verify-attestation", "--insecure-ignore-tlog=true", "--key", fmt.Sprintf("k8s://%s/%s", namespaceConfig.Name, cosignKeySecretName), "--type=spdx", imageRef)
			buf := new(bytes.Buffer)
			cmd.Stderr = buf
			err := cmd.Run()
			if err != nil {
				t.Fatalf("verify-attestation: %s - %s", err, buf.String())
			}
		}),
	); err != nil {
		t.Fatal(err)
	}
}

func generateCosignKey() ttr.TaskRunOpt {
	return func(c *ttr.TaskRunConfig) error {
		cmd := exec.Command("cosign", "generate-key-pair", fmt.Sprintf("k8s://%s/%s", namespaceConfig.Name, cosignKeySecretName))
		buf := new(bytes.Buffer)
		cmd.Stderr = buf
		cmd.Env = append(os.Environ(), "COSIGN_PASSWORD=s3cr3t")
		err := cmd.Run()
		if err != nil {
			return fmt.Errorf("generate-key-pair: %s - %s", err, buf.String())
		}
		return os.Remove("cosign.pub")
	}
}

func checkResultingFiles(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) {
	wantFiles := []string{
		fmt.Sprintf(".ods/artifacts/image-digests/%s.json", ctxt.Component),
		fmt.Sprintf(".ods/artifacts/sboms/%s.spdx", ctxt.Component),
	}
	for _, wf := range wantFiles {
		if _, err := os.Stat(filepath.Join(wsDir, wf)); os.IsNotExist(err) {
			t.Fatalf("Want %s, but got nothing", wf)
		}
	}
}

func checkTagFiles(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string, tags []string) {
	wantFiles := []string{}
	for _, tag := range tags {
		wantFiles = append(wantFiles, fmt.Sprintf(".ods/artifacts/image-digests/%s-%s.json", ctxt.Component, tag))
	}
	for _, wf := range wantFiles {
		if _, err := os.Stat(filepath.Join(wsDir, wf)); os.IsNotExist(err) {
			t.Fatalf("Want %s, but got nothing", wf)
		}
	}
}

func checkTags(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string, expectedTags []string) {
	// registry := "kind-registry.kind:5000"
	registry := "localhost:5000"
	tlsVerify := false
	args := []string{
		"inspect",
		`--format={{.RepoTags}}`,
		fmt.Sprintf("--tls-verify=%v", tlsVerify),
	}
	imageNsStreamSha := fmt.Sprintf("%s/%s:%s", ctxt.Namespace, ctxt.Component, ctxt.GitCommitSHA)
	imageRef := fmt.Sprintf("docker://%s/%s", registry, imageNsStreamSha)
	args = append(args, imageRef)

	stdout, _, err := runBuffered("skopeo", args)
	if err != nil {
		t.Fatalf("skopeo inspect %s: %s", fmt.Sprint(args), err)
	}
	tags, err := parseSkopeoInspectDigestTags(string(stdout))
	if err != nil {
		t.Fatalf("parse tags failed: %s", err)
	}
	for _, expectedTag := range expectedTags {
		if !slices.Contains(tags, expectedTag) {
			t.Fatalf("Expected tags=%s to be in actual tags=%s", fmt.Sprint(expectedTags), fmt.Sprint(tags))
		}
	}
}

func parseSkopeoInspectDigestTags(out string) ([]string, error) {
	t := strings.TrimSpace(out)
	if !(strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]")) {
		return nil, fmt.Errorf("skopeo inspect: unexpected tag response expecting tags to be in brackets %s", t)
	}
	t = t[1 : len(t)-1]
	// expecting t to have space separated tags.
	tags := strings.Split(t, " ")
	return tags, nil
}

func runSpecifiedImage(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string, image string) string {
	stdout, stderr, err := runBuffered("docker", []string{
		"run", "--rm",
		image,
	})
	if err != nil {
		t.Fatalf("could not run built image: %s, stderr: %s", err, string(stderr))
	}
	got := strings.TrimSpace(string(stdout))
	return got
}

func runResultingImage(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) string {
	got := runSpecifiedImage(t, ctxt, wsDir, getDockerImageTag(t, ctxt, wsDir))
	return got
}

func checkResultingImageHelloWorld(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) {
	got := runResultingImage(t, ctxt, wsDir)
	want := "Hello World"
	if got != want {
		t.Fatalf("Want %s, but got %s", want, got)
	}
}

func checkTaggedImageHelloWorld(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string, tag string) {
	image := fmt.Sprintf("localhost:5000/%s/%s:%s", ctxt.Namespace, ctxt.Component, tag)
	got := runSpecifiedImage(t, ctxt, wsDir, image)
	want := "Hello World"
	if got != want {
		t.Fatalf("Want %s, but got %s", want, got)
	}
}

func checkResultingImageHelloNexus(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) {
	got := runResultingImage(t, ctxt, wsDir)
	gotLines := strings.Split(got, "\n")

	ncc, err := newNexusClientConfig(
		newK8sClient(t), ctxt.Namespace, &logging.LeveledLogger{Level: logging.LevelDebug},
	)
	if err != nil {
		t.Fatalf("could not create Nexus client config: %s", err)
	}

	// nexusClient := tasktesting.NexusClientOrFatal(t, ctxt.Clients.KubernetesClientSet, ctxt.Namespace)
	nexusUrlString := string(ncc.BaseURL)
	nexusUrl, err := url.Parse(nexusUrlString)
	if err != nil {
		t.Fatalf("could not determine nexusUrl from nexusClient: %s", err)
	}

	wantUsername := "developer"
	if ncc.Username != wantUsername {
		t.Fatalf("Want %s, but got %s", wantUsername, ncc.Username)
	}

	wantSecret := "s3cr3t"
	if ncc.Password != wantSecret {
		t.Fatalf("Want %s, but got %s", wantSecret, ncc.Password)
	}

	want := []string{
		fmt.Sprintf("nexusUrl=%s", nexusUrlString),
		fmt.Sprintf("nexusUsername=%s", ncc.Username),
		fmt.Sprintf("nexusPassword=%s", ncc.Password),
		fmt.Sprintf("nexusAuth=%s:%s", ncc.Username, ncc.Password),
		fmt.Sprintf("nexusUrlWithAuth=http://%s:%s@%s", ncc.Username, ncc.Password, nexusUrl.Host),
		fmt.Sprintf("nexusHost=%s", nexusUrl.Host),
	}
	if diff := cmp.Diff(want, gotLines); diff != "" {
		t.Fatalf("context mismatch (-want +got):\n%s", diff)
	}
}

func checkResultingImageHelloBuildExtraArgs(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) {
	got := runResultingImage(t, ctxt, wsDir)
	gotLines := strings.Split(got, "\n")

	want := []string{
		fmt.Sprintf("firstArg=%s", "one"),
		fmt.Sprintf("secondArg=%s", "two"),
	}
	if diff := cmp.Diff(want, gotLines); diff != "" {
		t.Fatalf("context mismatch (-want +got):\n%s", diff)
	}
}

func getDockerImageTag(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) string {
	sha, err := getTrimmedFileContent(filepath.Join(wsDir, ".ods/git-commit-sha"))
	if err != nil {
		t.Fatalf("could not read git-commit-sha: %s", err)
	}
	return fmt.Sprintf("localhost:5000/%s/%s:%s", ctxt.Namespace, ctxt.Component, sha)
}

func generateArtifacts(t *testing.T, ctxt *pipelinectxt.ODSContext, tag string, wsDir string) {
	t.Logf("Generating artifacts for image %s", tag)
	generateImageArtifact(t, ctxt, tag, wsDir)
	generateImageSBOMArtifact(t, ctxt, wsDir)
}

func generateImageArtifact(t *testing.T, ctxt *pipelinectxt.ODSContext, tag string, wsDir string) {
	t.Logf("Generating image artifact")
	sha, err := getTrimmedFileContent(filepath.Join(wsDir, ".ods/git-commit-sha"))
	if err != nil {
		t.Fatalf("could not read git-commit-sha: %s", err)
	}
	ia := artifact.Image{
		Ref:        tag,
		Registry:   "kind-registry.kind:5000",
		Repository: ctxt.Namespace,
		Name:       ctxt.Component,
		Tag:        sha,
		Digest:     "abc",
	}
	imageArtifactFilename := fmt.Sprintf("%s.json", ctxt.Component)
	err = pipelinectxt.WriteJsonArtifact(ia, filepath.Join(wsDir, pipelinectxt.ImageDigestsPath), imageArtifactFilename)
	if err != nil {
		t.Fatalf("could not create image artifact: %s", err)
	}
}

func generateImageSBOMArtifact(t *testing.T, ctxt *pipelinectxt.ODSContext, wsDir string) {
	t.Logf("Generating image SBOM artifact")
	artifactsDir := filepath.Join(wsDir, pipelinectxt.SBOMsPath)
	sbomArtifactFilename := fmt.Sprintf("%s.%s", ctxt.Component, pipelinectxt.SBOMsFormat)
	err := os.MkdirAll(artifactsDir, 0755)
	if err != nil {
		t.Fatalf("could not create %s: %s", artifactsDir, err)
	}
	_, err = os.Create(filepath.Join(artifactsDir, sbomArtifactFilename))
	if err != nil {
		t.Fatalf("could not create image SBOM artifact: %s", err)
	}
}

func getTrimmedFileContent(filename string) (string, error) {
	content, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(content)), nil
}

func runBuffered(executable string, args []string) (outBytes, errBytes []byte, err error) {
	cmd := exec.Command(executable, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	outBytes = stdout.Bytes()
	errBytes = stderr.Bytes()
	return outBytes, errBytes, err
}

const (
	NexusConfigMapName     = "ods-nexus"
	NexusSecretName        = "ods-nexus-auth"
	NexusSecretUsernameKey = "username"
	NexusSecretPasswordKey = "password"
	NexusConfigMapURLKey   = "url"
)

// NewNexusClientConfig returns a *nexus.ClientConfig which is derived
// from the information about Nexus located in the given Kubernetes namespace.
func newNexusClientConfig(c kubernetes.Interface, namespace string, logger logging.LeveledLoggerInterface) (*nexus.ClientConfig, error) {
	nexusSecret, err := c.CoreV1().Secrets(namespace).
		Get(context.TODO(), NexusSecretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get Nexus secret: %w", err)
	}
	nexusConfigMap, err := c.CoreV1().ConfigMaps(namespace).
		Get(context.TODO(), NexusConfigMapName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("could not get Nexus config: %w", err)
	}
	nexusClientConfig := &nexus.ClientConfig{
		Username: string(nexusSecret.Data[NexusSecretUsernameKey]),
		Password: string(nexusSecret.Data[NexusSecretPasswordKey]),
		BaseURL:  nexusConfigMap.Data[NexusConfigMapURLKey],
		Logger:   logger,
	}
	return nexusClientConfig, nil
}

func newK8sClient(t *testing.T) *kubernetes.Clientset {
	home := homedir.HomeDir()
	kubeconfig := filepath.Join(home, ".kube", "config")
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		t.Fatal(err)
	}
	kubernetesClientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	return kubernetesClientset
}
