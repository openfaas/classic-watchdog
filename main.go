// Copyright (c) Alex Ellis 2017. All rights reserved.
// Licensed under the MIT license. See LICENSE file in the project root for full license information.

// Package main provides the OpenFaaS Classic Watchdog. The Classic Watchdog is a HTTP
// shim for serverless functions providing health-checking, graceful shutdowns,
// timeouts and a consistent logging experience.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/openfaas/classic-watchdog/metrics"
	"github.com/openfaas/classic-watchdog/types"
	"github.com/openfaas/faas-middleware/auth"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

var (
	acceptingConnections int32
)

func main() {
	var runHealthcheck bool
	var versionFlag bool

	flag.BoolVar(&versionFlag, "version", false, "Print the version and exit")
	flag.BoolVar(&runHealthcheck,
		"run-healthcheck",
		false,
		"Check for the a lock-file, when using an exec healthcheck. Exit 0 for present, non-zero when not found.")

	flag.Parse()

	if runHealthcheck {
		if lockFilePresent() {
			os.Exit(0)
		}

		fmt.Fprintf(os.Stderr, "unable to find lock file.\n")
		os.Exit(1)
	}

	printVersion()

	if versionFlag {
		return
	}

	atomic.StoreInt32(&acceptingConnections, 0)

	osEnv := types.OsEnv{}
	readConfig := ReadConfig{}
	config := readConfig.Read(osEnv)

	if len(config.faasProcess) == 0 {
		log.Panicln("Provide a valid process via fprocess environmental variable.")
		return
	}

	readTimeout := config.readTimeout
	writeTimeout := config.writeTimeout
	healthcheckInterval := config.healthcheckInterval

	s := &http.Server{
		Addr:           fmt.Sprintf(":%d", config.port),
		ReadTimeout:    readTimeout,
		WriteTimeout:   writeTimeout,
		MaxHeaderBytes: 1 << 20, // Max header of 1MB
	}

	httpMetrics := metrics.NewHttp()

	log.Printf("Timeouts: read: %s write: %s hard: %s health: %s.\n",
		readTimeout,
		writeTimeout,
		config.execTimeout,
		healthcheckInterval)
	log.Printf("Listening on port: %d\n", config.port)

	requestHandler := makeRequestHandler(&config)
	if config.jwtAuthentication {
		handler, err := makeJWTAuthHandler(config, requestHandler)
		if err != nil {
			log.Fatalf("Error creating JWTAuthMiddleware: %s", err.Error())
		}
		requestHandler = handler

	}

	http.HandleFunc("/_/health", makeHealthHandler())
	http.HandleFunc("/", metrics.InstrumentHandler(requestHandler, httpMetrics))

	metricsServer := metrics.MetricsServer{}
	metricsServer.Register(config.metricsPort)

	cancel := make(chan bool)

	go metricsServer.Serve(cancel)

	listenUntilShutdown(s, healthcheckInterval, writeTimeout, config.suppressLock, &httpMetrics)
}

// listenUntilShutdown will listen for HTTP requests until SIGTERM
// is sent at which point the code will wait `shutdownTimeout` before
// closing off connections and a futher `shutdownTimeout` before
// exiting
func listenUntilShutdown(s *http.Server, healthcheckInterval time.Duration, writeTimeout time.Duration, suppressLock bool, httpMetrics *metrics.Http) {

	idleConnsClosed := make(chan struct{})
	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGTERM)

		<-sig

		log.Printf("SIGTERM: no new connections in %s\n", healthcheckInterval.String())

		if err := markUnhealthy(); err != nil {
			log.Printf("Unable to mark server as unhealthy: %s\n", err.Error())
		}

		<-time.Tick(healthcheckInterval)

		connections := int64(testutil.ToFloat64(httpMetrics.InFlight))
		log.Printf("No new connections allowed, draining: %d requests\n", connections)

		// The maximum time to wait for active connections whilst shutting down is
		// equivalent to the maximum execution time i.e. writeTimeout.
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		defer cancel()

		if err := s.Shutdown(ctx); err != nil {
			log.Printf("Error in Shutdown: %v", err)
		}

		connections = int64(testutil.ToFloat64(httpMetrics.InFlight))

		log.Printf("Exiting. Active connections: %d\n", connections)

		close(idleConnsClosed)
	}()

	// Run the HTTP server in a separate go-routine.
	go func() {
		if err := s.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("Error ListenAndServe: %v", err)
			close(idleConnsClosed)
		}
	}()

	if suppressLock == false {
		path, writeErr := createLockFile()

		if writeErr != nil {
			log.Panicf("Cannot write %s. To disable lock-file set env suppress_lock=true.\n Error: %s.\n", path, writeErr.Error())
		}
	} else {
		log.Println("Warning: \"suppress_lock\" is enabled. No automated health-checks will be in place for your function.")

		atomic.StoreInt32(&acceptingConnections, 1)
	}

	<-idleConnsClosed
}

func markUnhealthy() error {
	atomic.StoreInt32(&acceptingConnections, 0)

	path := filepath.Join(os.TempDir(), ".lock")
	log.Printf("Removing lock-file : %s\n", path)
	removeErr := os.Remove(path)
	return removeErr
}

func printVersion() {
	sha := "unknown"
	if len(GitCommit) > 0 {
		sha = GitCommit
	}

	log.Printf("Version: %v\tSHA: %v\n", BuildVersion(), sha)
}

func makeJWTAuthHandler(c WatchdogConfig, next http.Handler) (http.Handler, error) {
	namespace, err := getFnNamespace()
	if err != nil {
		return nil, fmt.Errorf("failed to get function namespace: %w", err)
	}
	name, err := getFnName()
	if err != nil {
		return nil, fmt.Errorf("failed to get function name: %w", err)
	}

	authOpts := auth.JWTAuthOptions{
		Name:           name,
		Namespace:      namespace,
		LocalAuthority: c.jwtAuthLocal,
		Debug:          c.jwtAuthDebug,
	}

	return auth.NewJWTAuthMiddleware(authOpts, next)
}

func getFnName() (string, error) {
	name, ok := os.LookupEnv("OPENFAAS_NAME")
	if !ok || len(name) == 0 {
		return "", fmt.Errorf("env variable 'OPENFAAS_NAME' not set")
	}

	return name, nil
}

// getFnNamespace gets the namespace name from the env variable OPENFAAS_NAMESPACE
// or reads it from the service account if the env variable is not present
func getFnNamespace() (string, error) {
	if namespace, ok := os.LookupEnv("OPENFAAS_NAMESPACE"); ok {
		return namespace, nil
	}

	nsVal, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace")
	if err != nil {
		return "", err
	}
	return string(nsVal), nil
}
