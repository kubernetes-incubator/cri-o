package server

import (
	"context"
	"path/filepath"

	"github.com/cri-o/cri-o/internal/config/statsmgr"
	"github.com/cri-o/cri-o/internal/log"
	oci "github.com/cri-o/cri-o/internal/oci"
	"github.com/cri-o/cri-o/server/cri/types"
	"github.com/pkg/errors"
)

// ContainerStats returns stats of the container. If the container does not
// exist, the call returns an error.
func (s *Server) ContainerStats(ctx context.Context, req *types.ContainerStatsRequest) (*types.ContainerStatsResponse, error) {
	container, err := s.GetContainerFromShortID(req.ContainerID)
	if err != nil {
		return nil, err
	}
	stats, errs := s.CRIStatsForContainers(ctx, container)
	if len(errs) > 0 {
		return nil, errs[0]
	}
	// should never happen, but we should avoid the segfault
	if stats == nil || len(stats) != 1 {
		return nil, errors.Errorf("Unknown error happened finding container stats for %s", req.ContainerID)
	}

	return &types.ContainerStatsResponse{Stats: stats[0]}, nil
}

func (s *Server) CRIStatsForContainers(ctx context.Context, containers ...*oci.Container) ([]*types.ContainerStats, []error) {
	stats := make([]*types.ContainerStats, 0)
	errs := make([]error, 0)
	for _, c := range containers {
		sb := s.GetSandbox(c.Sandbox())
		if sb == nil {
			errs = append(errs, errors.Errorf("unable to get stats for container %s: sandbox %s not found", c.ID(), c.Sandbox()))
			continue
		}
		cgroup := sb.CgroupParent()

		ociStat, err := s.Runtime().ContainerStats(c, cgroup)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		stats = append(stats, s.buildContainerStats(ctx, ociStat, c))
	}
	return stats, errs
}

// buildContainerStats takes stats directly from the container, and attempts to inject the filesystem
// usage of the container.
// This is not taken care of by the container because we access information on the server level (storage driver).
func (s *Server) buildContainerStats(ctx context.Context, stats *oci.ContainerStats, container *oci.Container) *types.ContainerStats {
	// TODO: Fix this for other storage drivers. This will only work with overlay.
	var writableLayer *types.FilesystemUsage
	if s.ContainerServer.Config().RootConfig.Storage == "overlay" {
		diffDir := filepath.Join(filepath.Dir(container.MountPoint()), "diff")
		bytesUsed, inodeUsed, err := statsmgr.GetDiskUsageStats(diffDir)
		if err != nil {
			log.Warnf(ctx, "unable to get disk usage for container %s， %s", container.ID(), err)
		}
		writableLayer = &types.FilesystemUsage{
			Timestamp:  stats.SystemNano,
			FsID:       &types.FilesystemIdentifier{Mountpoint: container.MountPoint()},
			UsedBytes:  &types.UInt64Value{Value: bytesUsed},
			InodesUsed: &types.UInt64Value{Value: inodeUsed},
		}
	}
	return &types.ContainerStats{
		Attributes: &types.ContainerAttributes{
			ID: container.ID(),
			Metadata: &types.ContainerMetadata{
				Name:    container.Metadata().Name,
				Attempt: container.Metadata().Attempt,
			},
			Labels:      container.Labels(),
			Annotations: container.Annotations(),
		},
		CPU: &types.CPUUsage{
			Timestamp:            stats.SystemNano,
			UsageCoreNanoSeconds: &types.UInt64Value{Value: stats.CPUNano},
		},
		Memory: &types.MemoryUsage{
			Timestamp:       stats.SystemNano,
			WorkingSetBytes: &types.UInt64Value{Value: stats.WorkingSetBytes},
		},
		WritableLayer: writableLayer,
	}
}
