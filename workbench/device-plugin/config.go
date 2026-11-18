package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// Device maps one host device node to an extended resource. Pods request the
// resource; kubelet injects and allowlists the device node in the container.
type Device struct {
	Resource string `yaml:"resource"`
	Path     string `yaml:"path"`
}

type rawConfig struct {
	Devices []Device `yaml:"devices"`
}

var domainLabelRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]*[a-zA-Z0-9])?$`)
var resourceSegmentRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9._-]*[a-zA-Z0-9])*$`)

func (d Device) validate() error {
	domain, name, ok := strings.Cut(d.Resource, "/")
	if !ok || domain == "" || name == "" {
		return fmt.Errorf("resource %q must be <domain>/<name>", d.Resource)
	}

	for _, label := range strings.Split(domain, ".") {
		if !domainLabelRe.MatchString(label) {
			return fmt.Errorf("resource %q has invalid domain label %q", d.Resource, label)
		}
	}

	if !resourceSegmentRe.MatchString(name) {
		return fmt.Errorf("resource %q has invalid name", d.Resource)
	}

	if !strings.HasPrefix(d.Path, "/") {
		return fmt.Errorf("path %q must be absolute", d.Path)
	}

	return nil
}

func validateConfig(devices []Device) error {
	if len(devices) == 0 {
		return errors.New("no devices configured")
	}

	seenResource := map[string]bool{}
	seenPath := map[string]bool{}

	for _, d := range devices {
		if err := d.validate(); err != nil {
			return err
		}

		if seenResource[d.Resource] {
			return fmt.Errorf("duplicate resource %q", d.Resource)
		}

		if seenPath[d.Path] {
			return fmt.Errorf("duplicate path %q", d.Path)
		}

		seenResource[d.Resource] = true
		seenPath[d.Path] = true
	}

	return nil
}

func loadConfig(path string) ([]Device, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	var raw rawConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	if err := validateConfig(raw.Devices); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}

	return raw.Devices, nil
}

func socketName(resource string) string {
	r := strings.NewReplacer("/", "-", ".", "-")
	return r.Replace(resource) + ".sock"
}

func healthOf(path string) string {
	if _, err := os.Stat(path); err != nil {
		return "Unhealthy"
	}

	return "Healthy"
}
