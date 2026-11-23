// Package tools resolves, installs, and verifies the external media tools
// that musafoz manages, such as FFprobe.
package tools

import (
	"fmt"
	"slices"
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
	// URL is the versioned HTTPS download location of the archive, a .zip
	// or .tar.xz file. Several tools may share one archive.
	URL string
	// ArchiveSize and ArchiveSHA256 pin the archive.
	ArchiveSize   int64
	ArchiveSHA256 string
	// ArchiveEntry is the path of the executable inside the archive. When
	// empty, it is BinaryName.
	ArchiveEntry string
	// BinaryName is the file name of the installed executable.
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

// entry returns the path of the executable inside the archive.
func (a Artifact) entry() string {
	if a.ArchiveEntry != "" {
		return a.ArchiveEntry
	}
	return a.BinaryName
}

// archiveKey names the archive in lock and temporary file names. Tools that
// share an archive share its key, and therefore one lock and one download.
func (a Artifact) archiveKey() string {
	return a.ArchiveSHA256[:min(16, len(a.ArchiveSHA256))]
}

// SearchesPath reports whether tool resolution looks for a compatible tool
// on the PATH before using the managed build. On macOS the managed build is
// the default; on Linux and Windows an installed tool is used when it
// passes the compatibility checks.
func SearchesPath(goos string) bool {
	return goos == "linux" || goos == "windows"
}

// The Linux and Windows builds come from one BtbN FFmpeg-Builds release.
// BtbN keeps the last build of each month long term, so the pin uses one.
const (
	btbnRelease  = "https://github.com/BtbN/FFmpeg-Builds/releases/download/autobuild-2026-10-31-12-59/"
	btbnVersion  = "n9.0.2-39-g7adfbe40dc"
	btbnLinuxDir = "ffmpeg-" + btbnVersion + "-linux64-gpl-9.0"
	btbnLinuxURL = btbnRelease + btbnLinuxDir + ".tar.xz"
	btbnWinDir   = "ffmpeg-" + btbnVersion + "-win64-gpl-9.0"
	btbnWinURL   = btbnRelease + btbnWinDir + ".zip"
)

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
	{
		Tool:          "ffprobe",
		Version:       btbnVersion,
		GOOS:          "linux",
		GOARCH:        "amd64",
		URL:           btbnLinuxURL,
		ArchiveSize:   153175100,
		ArchiveSHA256: "73e29a56974ae5a651133a04584c7483b21afe941e17dc91d79814a758b608d3",
		ArchiveEntry:  btbnLinuxDir + "/bin/ffprobe",
		BinaryName:    "ffprobe",
		BinarySize:    173300552,
		BinarySHA256:  "ae9051af68f52e7ad5706b442577689c74c02da618ca1590caf1ac7199f2931c",
		VersionPrefix: "ffprobe version " + btbnVersion + "-20261031",
	},
	{
		Tool:          "ffmpeg",
		Version:       btbnVersion,
		GOOS:          "linux",
		GOARCH:        "amd64",
		URL:           btbnLinuxURL,
		ArchiveSize:   153175100,
		ArchiveSHA256: "73e29a56974ae5a651133a04584c7483b21afe941e17dc91d79814a758b608d3",
		ArchiveEntry:  btbnLinuxDir + "/bin/ffmpeg",
		BinaryName:    "ffmpeg",
		BinarySize:    173534088,
		BinarySHA256:  "384742b75419584d065bbf372613f9f7e8ccf22525b421c16a2ee679a78ee748",
		VersionPrefix: "ffmpeg version " + btbnVersion + "-20261031",
	},
	{
		Tool:          "ffprobe",
		Version:       btbnVersion,
		GOOS:          "windows",
		GOARCH:        "amd64",
		URL:           btbnWinURL,
		ArchiveSize:   195110184,
		ArchiveSHA256: "693f3e224bc184b40524a62ca47526cfebaf2504414021b92fe75f677ad868b9",
		ArchiveEntry:  btbnWinDir + "/bin/ffprobe.exe",
		BinaryName:    "ffprobe.exe",
		BinarySize:    165783040,
		BinarySHA256:  "075848c1bbb8b4e2e0b0ecba06e8ad06c889a8902ed219d6a9f5758c417ca108",
		VersionPrefix: "ffprobe version " + btbnVersion + "-20261031",
	},
	{
		Tool:          "ffmpeg",
		Version:       btbnVersion,
		GOOS:          "windows",
		GOARCH:        "amd64",
		URL:           btbnWinURL,
		ArchiveSize:   195110184,
		ArchiveSHA256: "693f3e224bc184b40524a62ca47526cfebaf2504414021b92fe75f677ad868b9",
		ArchiveEntry:  btbnWinDir + "/bin/ffmpeg.exe",
		BinaryName:    "ffmpeg.exe",
		BinarySize:    165984256,
		BinarySHA256:  "c930b63cb1ee8b7e2eaf9c15161160edd5c9acad34f0303b5855c8eb142a7e8d",
		VersionPrefix: "ffmpeg version " + btbnVersion + "-20261031",
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

// siblings returns the pinned artifacts stored in the same archive as a,
// a included.
func siblings(a Artifact) []Artifact {
	var list []Artifact
	for _, b := range artifacts {
		if b.URL == a.URL && b.ArchiveSHA256 == a.ArchiveSHA256 && b.Platform() == a.Platform() {
			list = append(list, b)
		}
	}
	if !slices.ContainsFunc(list, func(b Artifact) bool { return b.Tool == a.Tool }) {
		list = append([]Artifact{a}, list...)
	}
	return list
}
