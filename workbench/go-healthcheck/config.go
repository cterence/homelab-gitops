package main

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type Targets struct {
	HTTP       []string `yaml:"http"`
	PostgreSQL []string `yaml:"postgresql"`
	Redis      []string `yaml:"redis"`
}

type Config struct {
	Name                         string  `yaml:"name"`
	Port                         string  `yaml:"port,omitempty"`
	Version                      string  `yaml:"version"`
	Timeout                      int     `yaml:"timeout"`
	Targets                      Targets `yaml:"targets"`
	HTTPClientCertPath           string  `yaml:"httpClientCertPath,omitempty"`
	HTTPClientKeyPath            string  `yaml:"httpClientKeyPath,omitempty"`
	HTTPStatusCodeErrorThreshold int     `yaml:"httpStatusCodeErrorThreshold,omitempty"`
}

func (c *Config) Load() error {
	configYaml, err := os.ReadFile("config.yaml")
	if err != nil {
		return fmt.Errorf("failed to open config file: %v", err)
	}

	err = yaml.Unmarshal(configYaml, &c)
	if err != nil {
		return fmt.Errorf("failed to unmarshal YAML config: %v", err)
	}

	if c.Port == "" {
		c.Port = "3000"
	}

	return nil
}
