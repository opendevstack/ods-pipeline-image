package main

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/shlex"
	"github.com/opendevstack/ods-pipeline-image/internal/image"
	"github.com/opendevstack/ods-pipeline/pkg/artifact"
	"github.com/opendevstack/ods-pipeline/pkg/pipelinectxt"
)

type PackageStep func(d *packageImage) (*packageImage, error)

func (d *packageImage) runSteps(steps ...PackageStep) error {
	var skip *skipRemainingSteps
	var err error
	for _, step := range steps {
		d, err = step(d)
		if err != nil {
			if errors.As(err, &skip) {
				d.logger.Infof(err.Error())
				return nil
			}
			return err
		}
	}
	return nil
}

func setupContext() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		ctxt := &pipelinectxt.ODSContext{}
		err := ctxt.ReadCache(p.opts.checkoutDir)
		if err != nil {
			return p, fmt.Errorf("read cache: %w", err)
		}
		p.ctxt = ctxt

		// TLS verification of the KinD registry is not possible at the moment as
		// requests error out with "server gave HTTP response to HTTPS client".
		if strings.HasPrefix(p.opts.registry, "kind-registry.kind") {
			p.opts.tlsVerify = false
		}

		return p, nil
	}
}

func setExtraTags() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		extraTagsSpecified, err := shlex.Split(p.opts.extraTags)
		if err != nil {
			return p, fmt.Errorf("parse extra tags (%s): %w", p.opts.extraTags, err)
		}
		p.parsedExtraTags = extraTagsSpecified
		return p, nil
	}
}

func setImageId() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		p.imageId = image.CreateImageIdentity(p.ctxt, p.opts.imageNamespace, p.opts.imageStream)
		return p, nil
	}
}

// skipIfImageArtifactExists informs to skip next steps if ODS image artifact is already in place.
// In future we might want to check all the expected artifacts, that must exist to do skip properly.
func skipIfImageArtifactExists() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Printf("Checking if image artifact for %s exists already ...\n", p.imageName())
		err := imageArtifactExists(p)
		if err == nil {
			return p, &skipRemainingSteps{"image artifact exists already"}
		}
		return p, nil
	}
}

func buildImageAndGenerateTar() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Printf("Building image %s ...\n", p.imageName())
		err := p.buildahBuild(os.Stdout, os.Stderr)
		if err != nil {
			return p, fmt.Errorf("buildah bud: %w", err)
		}
		fmt.Printf("Creating local tar folder for image %s ...\n", p.imageName())
		err = p.buildahPushTar(os.Stdout, os.Stderr)
		if err != nil {
			return p, fmt.Errorf("buildah push tar: %w", err)
		}
		d, err := getImageDigestFromFile(p.opts.checkoutDir)
		if err != nil {
			return p, err
		}
		p.imageDigest = d
		return p, nil
	}
}

func generateSBOM() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Println("Generating image SBOM with trivy scanner ...")
		err := p.generateImageSBOM()
		if err != nil {
			return p, fmt.Errorf("generate SBOM: %w", err)
		}
		return p, nil
	}
}

func pushImage() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Printf("Pushing image %s ...\n", p.imageName())
		err := p.buildahPush(os.Stdout, os.Stderr)
		if err != nil {
			return p, fmt.Errorf("buildah push: %w", err)
		}
		return p, nil
	}
}

func signImage(cosignKey string) PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		if cosignKey != "" {
			i := imageRef(p.artifactImage())
			c := NewCosignClient(cosignKey)
			log.Printf("Signing image %s with %s ...\n", p.imageName(), cosignKey)
			if err := c.Sign(i); err != nil {
				return p, fmt.Errorf("signing: %s", err)
			}
			log.Println("Generating SBOM attestation ...")
			if err := c.Attest(i, pipelinectxt.SBOMsFormat, p.sbomFile); err != nil {
				return p, fmt.Errorf("attesting SBOM: %s", err)
			}
		}
		return p, nil
	}
}

func storeArtifact() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Println("Writing image artifact ...")
		imageArtifactFilename := fmt.Sprintf("%s.json", p.imageNameNoSha())
		err := pipelinectxt.WriteJsonArtifact(p.artifactImage(), pipelinectxt.ImageDigestsPath, imageArtifactFilename)
		if err != nil {
			return p, err
		}

		fmt.Println("Writing SBOM artifact ...")
		err = pipelinectxt.CopyArtifact(p.sbomFile, pipelinectxt.SBOMsPath)
		if err != nil {
			return p, fmt.Errorf("copy SBOM report to artifacts: %w", err)
		}

		return p, nil
	}
}

func storeResults() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		fmt.Println("Writing image-ref result ...")
		err := os.WriteFile(tektonResultsImageRefFile, []byte(imageRef(p.artifactImage())), 0644)
		return p, err
	}
}

func processExtraTags() PackageStep {
	return func(p *packageImage) (*packageImage, error) {
		if len(p.parsedExtraTags) > 0 {
			p.logger.Infof("Processing extra tags: %+q", p.parsedExtraTags)
			for _, extraTag := range p.parsedExtraTags {
				err := imageTagArtifactExists(p, extraTag)
				if err == nil {
					p.logger.Infof("Artifact exists for tag: %s", extraTag)
					continue
				}
				p.logger.Infof("pushing extra tag: %s", extraTag)
				imageExtraTag := p.imageId.Tag(extraTag)
				err = p.skopeoTag(&imageExtraTag, os.Stdout, os.Stderr)
				if err != nil {
					return p, fmt.Errorf("skopeo push failed: %w", err)
				}

				p.logger.Infof("Writing image artifact for tag: %s", extraTag)
				image := p.artifactImageForTag(extraTag)
				filename := fmt.Sprintf("%s-%s.json", p.imageId.ImageStream, extraTag)
				err = pipelinectxt.WriteJsonArtifact(image, pipelinectxt.ImageDigestsPath, filename)
				if err != nil {
					return p, err
				}
			}
		}
		return p, nil
	}
}

func imageTagArtifactExists(p *packageImage, tag string) error {
	imageArtifactsDir := filepath.Join(p.opts.checkoutDir, pipelinectxt.ImageDigestsPath)
	filename := fmt.Sprintf("%s-%s.json", p.imageId.ImageStream, tag)
	_, err := os.Stat(filepath.Join(imageArtifactsDir, filename))
	return err
}

func imageRef(i artifact.Image) string {
	return fmt.Sprintf("%s/%s/%s@%s", i.Registry, i.Repository, i.Name, i.Digest)
}
