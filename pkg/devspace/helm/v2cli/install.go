package v2cli

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/devspace-cloud/devspace/pkg/devspace/config/versions/latest"
	"github.com/devspace-cloud/devspace/pkg/devspace/helm/types"
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

const stableChartRepo = "https://kubernetes-charts.storage.googleapis.com"

// InstallChart installs the given chart via helm v2
func (c *client) InstallChart(releaseName string, releaseNamespace string, values map[interface{}]interface{}, helmConfig *latest.HelmConfig) (*types.Release, error) {
	// Make sure helm binary path is set & tiller is ready
	err := c.ensureHelmBinary(helmConfig)
	if err != nil {
		return nil, err
	}

	err = c.ensureTiller()
	if err != nil {
		return nil, err
	}

	f, err := ioutil.TempFile("", "")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(f.Name())
	defer f.Close()

	out, err := yaml.Marshal(values)
	if err != nil {
		return nil, errors.Wrap(err, "marshal values")
	}

	_, err = f.Write(out)
	if err != nil {
		return nil, err
	}

	if releaseNamespace == "" {
		releaseNamespace = c.kubeClient.Namespace()
	}

	chartName, chartRepo := chartNameAndRepo(helmConfig)
	args := []string{"upgrade", releaseName, chartName, "--output", "json", "--namespace", releaseNamespace, "--values", f.Name(), "--install", "--kube-context", c.kubeClient.CurrentContext(), "--tiller-namespace", c.tillerNamespace}

	// Chart settings
	if chartRepo != "" {
		args = append(args, "--repo", chartRepo)
	}
	if helmConfig.Chart.Version != "" {
		args = append(args, "--version", helmConfig.Chart.Version)
	}
	if helmConfig.Chart.Username != "" {
		args = append(args, "--username", helmConfig.Chart.Username)
	}
	if helmConfig.Chart.Password != "" {
		args = append(args, "--password", helmConfig.Chart.Password)
	}

	// Upgrade options
	if helmConfig.Atomic || helmConfig.CleanupOnFail {
		args = append(args, "--cleanup-on-fail")
	}
	if helmConfig.Wait {
		args = append(args, "--wait")
	}
	if helmConfig.Timeout != nil {
		args = append(args, "--timeout", strconv.FormatInt(*helmConfig.Timeout, 10))
	}
	if helmConfig.Recreate {
		args = append(args, "--recreate-pods")
	}
	if helmConfig.Force {
		args = append(args, "--force")
	}
	if helmConfig.DisableHooks {
		args = append(args, "--no-hooks")
	}

	var result []byte
	for {
		result, err = c.exec(c.helmPath, args).CombinedOutput()
		if err != nil {
			if strings.Index(string(result), "could not find a ready tiller pod") != -1 {
				time.Sleep(time.Second * 3)
				err = c.ensureTiller()
				if err != nil {
					return nil, err
				}

				continue
			}

			return nil, fmt.Errorf("error upgrading chart: %s => %v", string(result), err)
		}

		break
	}

	releases, err := c.ListReleases(helmConfig)
	if err != nil {
		return nil, err
	}

	for _, r := range releases {
		if r.Name == releaseName && r.Namespace == releaseNamespace {
			return r, nil
		}
	}

	return nil, nil
}

func (c *client) Template(releaseName, releaseNamespace string, values map[interface{}]interface{}, helmConfig *latest.HelmConfig) (string, error) {
	err := c.ensureHelmBinary(helmConfig)
	if err != nil {
		return "", err
	}

	cleanup, chartDir, err := c.fetch(helmConfig)
	if err != nil {
		return "", err
	} else if cleanup {
		defer os.RemoveAll(filepath.Dir(chartDir))
	}

	if releaseNamespace == "" {
		releaseNamespace = c.kubeClient.Namespace()
	}

	f, err := ioutil.TempFile("", "")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(f.Name())
	defer f.Close()

	out, err := yaml.Marshal(values)
	if err != nil {
		return "", errors.Wrap(err, "marshal values")
	}

	_, err = f.Write(out)
	if err != nil {
		return "", err
	}

	args := []string{"template", chartDir, "--name", releaseName, "--namespace", releaseNamespace, "--values", f.Name(), "--kube-context", c.kubeClient.CurrentContext(), "--tiller-namespace", c.tillerNamespace}
	result, err := c.exec(c.helmPath, args).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("error during helm template: %s => %v", string(result), err)
	}

	return string(result), nil
}

func (c *client) fetch(helmConfig *latest.HelmConfig) (bool, string, error) {
	chartName, chartRepo := chartNameAndRepo(helmConfig)
	if chartRepo == "" {
		return false, chartName, nil
	}

	tempFolder, err := ioutil.TempDir("", "")
	if err != nil {
		return false, "", err
	}

	args := []string{"fetch", chartName, "--repo", chartRepo, "--untar", "--untardir", tempFolder}
	if helmConfig.Chart.Version != "" {
		args = append(args, "--version", helmConfig.Chart.Version)
	}
	if helmConfig.Chart.Username != "" {
		args = append(args, "--username", helmConfig.Chart.Username)
	}
	if helmConfig.Chart.Password != "" {
		args = append(args, "--password", helmConfig.Chart.Password)
	}

	out, err := c.exec(c.helmPath, args).CombinedOutput()
	if err != nil {
		os.RemoveAll(tempFolder)
		return false, "", fmt.Errorf("error running helm fetch: %s => %v", string(out), err)
	}

	return true, filepath.Join(tempFolder, chartName), nil
}

func (c *client) DeleteRelease(releaseName string, releaseNamespace string, helmConfig *latest.HelmConfig) error {
	err := c.ensureHelmBinary(helmConfig)
	if err != nil {
		return err
	}

	err = c.ensureTiller()
	if err != nil {
		return err
	}

	args := []string{"delete", releaseName, "--kube-context", c.kubeClient.CurrentContext(), "--tiller-namespace", c.tillerNamespace, "--purge"}
	out, err := c.exec(c.helmPath, args).CombinedOutput()
	if err != nil {
		return fmt.Errorf("error deleting release: %s => %v", string(out), err)
	}

	return nil
}

func (c *client) ListReleases(helmConfig *latest.HelmConfig) ([]*types.Release, error) {
	err := c.ensureHelmBinary(helmConfig)
	if err != nil {
		return nil, err
	}

	err = c.ensureTiller()
	if err != nil {
		return nil, err
	}

	args := []string{"list", "--kube-context", c.kubeClient.CurrentContext(), "--tiller-namespace", c.tillerNamespace, "--output", "json"}
	out, err := c.exec(c.helmPath, args).CombinedOutput()
	if err != nil {
		if strings.Index(string(out), "could not find a ready tiller pod") > -1 {
			c.log.Info("Couldn't find a ready tiller pod, will wait 3 seconds more")
			time.Sleep(time.Second * 3)
			return c.ListReleases(helmConfig)
		}

		return nil, fmt.Errorf("error listing releases: %s => %v", string(out), err)
	}

	releases := &struct {
		Releases []struct {
			Name      string `yaml:"Name"`
			Namespace string `yaml:"Namespace"`
			Status    string `yaml:"Status"`
			Revision  int32  `yaml:"Revision"`
			Updated   string `yaml:"Updated"`
		} `yaml:"Releases"`
	}{}
	err = yaml.Unmarshal(out, releases)
	if err != nil {
		return nil, err
	}

	result := []*types.Release{}
	for _, release := range releases.Releases {
		t, err := time.Parse(time.ANSIC, release.Updated)
		if err != nil {
			return nil, err
		}

		result = append(result, &types.Release{
			Name:         release.Name,
			Namespace:    release.Namespace,
			Status:       release.Status,
			Version:      release.Revision,
			LastDeployed: t,
		})
	}

	return result, nil
}

func chartNameAndRepo(helmConfig *latest.HelmConfig) (string, string) {
	chartName := strings.TrimSpace(helmConfig.Chart.Name)
	chartRepo := helmConfig.Chart.RepoURL
	if strings.HasPrefix(chartName, "stable/") && chartRepo == "" {
		chartName = chartName[7:]
		chartRepo = stableChartRepo
	}

	return chartName, chartRepo
}
