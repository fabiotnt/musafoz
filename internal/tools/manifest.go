// Package tools resolves, installs, and verifies the external media tools
// that musafoz manages, such as FFprobe.
package tools

import (
	"fmt"
	"strings"
)

// Artifact describes one pinned, downloadable build of a managed tool.
type Artifact struct {
	// Tool is the tool name, for example "ffprobe".
	Tool string
	// Version is the exact upstream release version.
	Version string
	// GOOS and GOARCH identify the platform the build runs on.
	GOOS   string
	GOARCH string
	// URL is the versioned HTTPS download location of the ZIP archive.
	URL string
	// ArchiveSize and ArchiveSHA256 pin the ZIP archive.
	ArchiveSize   int64
	ArchiveSHA256 string
	// BinaryName is the name of the executable entry inside the archive.
	BinaryName string
	// BinarySize and BinarySHA256 pin the extracted executable.
	BinarySize   int64
	BinarySHA256 string
	// VersionPrefix is the expected start of the first line of "<tool> -version".
	VersionPrefix string
}

// Platform returns the artifact platform as "goos/goarch".
func (a Artifact) Platform() string {
	return a.GOOS + "/" + a.GOARCH
}

// artifacts lists the pinned FFmpeg and FFprobe builds. See
// docs/decisions for how each entry was verified.
var artifacts = []Artifact{
	{
		Tool:          "ffprobe",
		Version:       "9.1",
		GOOS:          "darwin",
		GOARCH:        "amd64",
		URL:           "https://evermeet.cx/ffmpeg/ffprobe-9.1.zip",
		ArchiveSize:   26451133,
		ArchiveSHA256: "fe81b028c213d6387a5c6b315692b3ceddcd31eb6dae55bbcae257a862573918",
		BinaryName:    "ffprobe",
		BinarySize:    81576216,
		BinarySHA256:  "272759075855b9d34a37b60dfef2274f599f12fe97735019aa5c763a2bf89e75",
		VersionPrefix: "ffprobe version 9.1-tessus",
	},
	{
		Tool:          "ffmpeg",
		Version:       "9.1",
		GOOS:          "darwin",
		GOARCH:        "amd64",
		URL:           "https://evermeet.cx/ffmpeg/ffmpeg-9.1.zip",
		ArchiveSize:   26555632,
		ArchiveSHA256: "9962f7b3c01af9ac89673c91df9ef998343a280ac0857c295e2d2a301820598a",
		BinaryName:    "ffmpeg",
		BinarySize:    81778888,
		BinarySHA256:  "1e3026f19f8b8d67c5fa0987aa3bac376d45c7b0e0485dc22515fc011546f8c2",
		VersionPrefix: "ffmpeg version 9.1-tessus",
	},
}

// FFprobeArtifact returns the pinned FFprobe build for the given platform.
func FFprobeArtifact(goos, goarch string) (Artifact, error) {
	return LookupArtifact(ToolFFprobe, goos, goarch)
}

// LookupArtifact returns the pinned build of tool for the given platform.
func LookupArtifact(tool, goos, goarch string) (Artifact, error) {
	for _, a := range artifacts {
		if a.Tool == tool && a.GOOS == goos && a.GOARCH == goarch {
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("no managed %s build for %s/%s; supported platforms: %s; set --%s or %s to use your own %s",
		tool, goos, goarch, supportedPlatforms(tool), tool, EnvVar(tool), tool)
}

func supportedPlatforms(tool string) string {
	var list []string
	for _, a := range artifacts {
		if a.Tool == tool {
			list = append(list, a.Platform())
		}
	}
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}
