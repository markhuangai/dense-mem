package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/service"
)

const harnessConfigLimit = 1 << 20

type harnessConfig struct {
	Profiles []domain.OAuthProtectedResourceProfile `json:"profiles"`
}

type harnessOptions struct {
	listen        string
	publicBaseURL string
	configPath    string
	tlsCertPath   string
	tlsKeyPath    string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		log.Printf("oauth compatibility harness stopped: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, errorOutput io.Writer) error {
	options, err := parseHarnessOptions(args, errorOutput)
	if err != nil {
		return err
	}
	publicBaseURL, err := validatePublicBaseURL(options.publicBaseURL)
	if err != nil {
		return fmt.Errorf("invalid --public-base-url: %w", err)
	}
	config, err := loadHarnessConfig(options.configPath)
	if err != nil {
		return fmt.Errorf("load OAuth compatibility config: %w", err)
	}
	validator, err := service.NewOAuthProtectedResourceValidator(config.Profiles, service.OAuthProtectedResourceValidatorOptions{})
	if err != nil {
		return fmt.Errorf("validate OAuth compatibility config: %w", err)
	}
	handler, err := newHarnessHandler(publicBaseURL, config.Profiles, validator)
	if err != nil {
		return err
	}

	server := &http.Server{
		Addr:              options.listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    600 * 1024,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		ErrorLog:          log.New(errorOutput, "oauth-compat-harness: ", log.LstdFlags),
	}
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- server.ListenAndServeTLS(options.tlsCertPath, options.tlsKeyPath)
	}()

	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown OAuth compatibility harness: %w", err)
		}
		return nil
	}
}

func parseHarnessOptions(args []string, errorOutput io.Writer) (harnessOptions, error) {
	var options harnessOptions
	flags := flag.NewFlagSet("oauth-compat-harness", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	flags.StringVar(&options.listen, "listen", ":9445", "HTTPS listen address")
	flags.StringVar(&options.publicBaseURL, "public-base-url", "", "trusted public HTTPS base URL")
	flags.StringVar(&options.configPath, "config", "", "protected-resource profile JSON file")
	flags.StringVar(&options.tlsCertPath, "tls-cert", "", "TLS certificate file")
	flags.StringVar(&options.tlsKeyPath, "tls-key", "", "TLS private key file")
	if err := flags.Parse(args); err != nil {
		return harnessOptions{}, err
	}
	if flags.NArg() != 0 {
		return harnessOptions{}, fmt.Errorf("unexpected positional arguments")
	}
	for name, value := range map[string]string{
		"--listen":          options.listen,
		"--public-base-url": options.publicBaseURL,
		"--config":          options.configPath,
		"--tls-cert":        options.tlsCertPath,
		"--tls-key":         options.tlsKeyPath,
	} {
		if value == "" {
			return harnessOptions{}, fmt.Errorf("%s is required", name)
		}
	}
	return options, nil
}

func loadHarnessConfig(path string) (harnessConfig, error) {
	file, err := os.Open(path)
	if err != nil {
		return harnessConfig{}, err
	}
	defer file.Close()
	var config harnessConfig
	if err := jsonstrict.Decode(file, &config, harnessConfigLimit); err != nil {
		return harnessConfig{}, err
	}
	return config, nil
}
