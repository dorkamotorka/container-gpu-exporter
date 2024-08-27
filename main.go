package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/NVIDIA/go-nvml/pkg/nvml"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	containerGpuMemoryUsed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "docker_gpu_memory_usage",
			Help: "GPU memory used by Docker container",
		},
		[]string{"pid", "container_id", "container"},
	)
	containerGpuMemoryPercUsed = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "docker_gpu_memory_perc_usage",
			Help: "GPU memory in percentage used by container",
		},
		[]string{"pid", "container_id", "container"},
	)
)

func main() {
	// Register Prometheus metrics
	reg := prometheus.NewRegistry()
	reg.MustRegister(containerGpuMemoryUsed)
	reg.MustRegister(containerGpuMemoryPercUsed)

	// Initialize NVML
	ret := nvml.Init()
	if ret != nvml.SUCCESS {
		log.Fatalf("Unable to initialize NVML: %v", nvml.ErrorString(ret))
	}
	defer func() {
		ret := nvml.Shutdown()
		if ret != nvml.SUCCESS {
			log.Fatalf("Unable to shutdown NVML: %v", nvml.ErrorString(ret))
		}
	}()

	// Create a Docker client
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatalf("Failed to create Docker client: %v", err)
	}
	defer cli.Close()

	// Start Prometheus metrics server
	go func() {
		handler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
		http.Handle("/metrics", handler)
		log.Fatal(http.ListenAndServe(":8000", nil))
	}()

	for {
		// List running containers
		containers, err := cli.ContainerList(context.Background(), container.ListOptions{All: true})
		if err != nil {
			log.Fatalf("Failed to list containers: %v", err)
		}

		// Get device count
		count, ret := nvml.DeviceGetCount()
		if ret != nvml.SUCCESS {
			log.Fatalf("Unable to get device count: %v", nvml.ErrorString(ret))
		}

		// Iterate over devices
		for di := 0; di < count; di++ {
			device, ret := nvml.DeviceGetHandleByIndex(di)
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to get device at index %d: %v", di, nvml.ErrorString(ret))
			}

			memoryInfo, ret := device.GetMemoryInfo()
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to get device memory at index %d: %v", di, nvml.ErrorString(ret))
			}

			// Get running processes on device
			processInfos, ret := device.GetComputeRunningProcesses()
			if ret != nvml.SUCCESS {
				log.Fatalf("Unable to get process info for device at index %d: %v", di, nvml.ErrorString(ret))
			}

			// Iterate over running processes
			for _, processInfo := range processInfos {
				// Iterate over containers
				for _, container := range containers {
					// Inspect each container to get detailed information
					containerInfo, err := cli.ContainerInspect(context.Background(), container.ID)
					if err != nil {
						log.Printf("Failed to inspect container %s: %v", container.ID, err)
						continue
					}

					// Extract required information
					pid := containerInfo.State.Pid
					containerID := container.ID[:12]
					containerName := containerInfo.Name[1:]

					if pid == int(processInfo.Pid) {
						// Set Prometheus metrics
						containerGpuMemoryUsed.WithLabelValues(fmt.Sprintf("%d", pid), containerID, containerName).Set(float64(processInfo.UsedGpuMemory))

						percent := (float64(processInfo.UsedGpuMemory) / float64(memoryInfo.Total)) * 100
						containerGpuMemoryPercUsed.WithLabelValues(fmt.Sprintf("%d", pid), containerID, containerName).Set(percent)
					}
				}
			}
		}
		time.Sleep(30 * time.Second)
	}
}
