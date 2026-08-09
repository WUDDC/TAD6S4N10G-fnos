package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"fnos-powerguard/internal/powerguard"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "serve":
		err = serve(os.Args[2:])
	case "probe":
		err = probe(os.Args[2:])
	case "init":
		err = initialize(os.Args[2:])
	case "apply":
		err = apply(os.Args[2:])
	case "restore":
		err = restore(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "powerguard:", err)
		os.Exit(1)
	}
}

func commonFlags(name string, args []string) (*powerguard.Manager, *flag.FlagSet, error) {
	set := flag.NewFlagSet(name, flag.ContinueOnError)
	config := set.String("config", "", "config file")
	state := set.String("state", "", "state file")
	root := set.String("sys-root", "/", "alternate system root (testing only)")
	if err := set.Parse(args); err != nil {
		return nil, nil, err
	}
	if *config == "" || *state == "" {
		return nil, nil, errors.New("--config and --state are required")
	}
	return &powerguard.Manager{Root: *root, ConfigPath: *config, StatePath: *state, Version: version}, set, nil
}

func initialize(args []string) error {
	manager, _, err := commonFlags("init", args)
	if err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	if _, err := manager.LoadOrCreateConfig(); err != nil {
		return err
	}
	return manager.ApplyCurrent()
}

func apply(args []string) error {
	manager, _, err := commonFlags("apply", args)
	if err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	return manager.ApplyCurrent()
}

func restore(args []string) error {
	manager, _, err := commonFlags("restore", args)
	if err != nil {
		return err
	}
	if err := requireRoot(); err != nil {
		return err
	}
	return manager.Restore()
}

func probe(args []string) error {
	set := flag.NewFlagSet("probe", flag.ContinueOnError)
	root := set.String("sys-root", "/", "alternate system root")
	asJSON := set.Bool("json", false, "JSON output")
	if err := set.Parse(args); err != nil {
		return err
	}
	manager := &powerguard.Manager{Root: *root, Version: version}
	model, err := manager.CPUModel()
	if err != nil {
		return err
	}
	profile, err := powerguard.DetectProfile(model)
	if err != nil {
		return err
	}
	packages, err := manager.DiscoverPackages()
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"cpu_model": model, "profile": profile, "packages": packages})
	}
	fmt.Printf("CPU: %s\nProfile: %s\nRAPL packages: %d\n", model, profile.ID, len(packages))
	return nil
}

func serve(args []string) error {
	set := flag.NewFlagSet("serve", flag.ContinueOnError)
	config := set.String("config", "", "config file")
	state := set.String("state", "", "state file")
	socket := set.String("socket", "", "Unix socket")
	webRoot := set.String("web-root", "", "static web root")
	logPath := set.String("log", "", "log file")
	root := set.String("sys-root", "/", "alternate system root (testing only)")
	if err := set.Parse(args); err != nil {
		return err
	}
	if *config == "" || *state == "" || *socket == "" || *webRoot == "" {
		return errors.New("--config, --state, --socket and --web-root are required")
	}
	if err := requireRoot(); err != nil {
		return err
	}
	logger := log.Default()
	if *logPath != "" {
		if err := os.MkdirAll(filepath.Dir(*logPath), 0o700); err != nil {
			return err
		}
		file, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer file.Close()
		logger = log.New(file, "", log.LstdFlags|log.LUTC)
	}
	manager := &powerguard.Manager{Root: *root, ConfigPath: *config, StatePath: *state, Version: version}
	cfg, err := manager.LoadOrCreateConfig()
	if err != nil {
		return err
	}
	if err := manager.ApplyCurrent(); err != nil {
		return err
	}
	logger.Printf("started version=%s interval=%ds", version, cfg.ReapplySeconds)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		server := &powerguard.Server{Manager: manager, Socket: *socket, WebRoot: *webRoot, BasePath: "/app/powerguard", Logger: logger}
		done <- server.ListenAndServe()
	}()
	go reapplyLoop(ctx, manager, logger)

	select {
	case <-ctx.Done():
		logger.Printf("stopping: restoring captured power limits")
		return manager.Restore()
	case err := <-done:
		if err != nil {
			return err
		}
		return nil
	}
}

func reapplyLoop(ctx context.Context, manager *powerguard.Manager, logger *log.Logger) {
	for {
		cfg, err := manager.LoadOrCreateConfig()
		interval := 30 * time.Second
		if err == nil && cfg.ReapplySeconds >= 5 && cfg.ReapplySeconds <= 300 {
			interval = time.Duration(cfg.ReapplySeconds) * time.Second
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := manager.ApplyCurrent(); err != nil {
				logger.Printf("reapply failed: %v", err)
			}
		}
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: powerguard <serve|probe|init|apply|restore|version> [options]")
}
