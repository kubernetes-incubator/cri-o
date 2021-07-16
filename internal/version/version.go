package version

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/blang/semver"
	"github.com/cri-o/cri-o/utils"
	"github.com/google/renameio"
	json "github.com/json-iterator/go"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

// Version is the version of the build.
const Version = "1.20.4"

// Variables injected during build-time
var (
	gitCommit    string // sha1 from git, output of $(git rev-parse HEAD)
	gitTreeState string // state of git tree, either "clean" or "dirty"
	buildDate    string // build date in ISO8601 format, output of $(date -u +'%Y-%m-%dT%H:%M:%SZ')
)

type Info struct {
	Version      string `json:"version,omitempty"`
	GitCommit    string `json:"gitCommit,omitempty"`
	GitTreeState string `json:"gitTreeState,omitempty"`
	BuildDate    string `json:"buildDate,omitempty"`
	GoVersion    string `json:"goVersion,omitempty"`
	Compiler     string `json:"compiler,omitempty"`
	Platform     string `json:"platform,omitempty"`
	Linkmode     string `json:"linkmode,omitempty"`
}

// ShouldCrioWipe opens the version file, and parses it and the version string
// If there is a parsing error, then crio should wipe, and the error is returned.
// if parsing is successful, it compares the major and minor versions
// and returns whether the major and minor versions are the same.
// If they differ, then crio should wipe.
func ShouldCrioWipe(versionFileName string) (bool, error) {
	return shouldCrioWipe(versionFileName, Version)
}

// shouldCrioWipe is an internal function for testing purposes
func shouldCrioWipe(versionFileName, versionString string) (bool, error) {
	f, err := os.Open(versionFileName)
	if err != nil {
		return true, errors.Errorf("version file %s not found: %v", versionFileName, err)
	}
	r := bufio.NewReader(f)
	versionBytes, err := ioutil.ReadAll(r)
	if err != nil {
		return true, errors.Errorf("reading version file %s failed: %v", versionFileName, err)
	}

	// parse the version that was laid down by a previous invocation of crio
	var oldVersion semver.Version
	if err := oldVersion.UnmarshalJSON(versionBytes); err != nil {
		return true, errors.Errorf("version file %s malformatted: %v", versionFileName, err)
	}

	// parse the version of the current binary
	newVersion, err := parseVersionConstant(versionString, "")
	if err != nil {
		return true, errors.Errorf("version constant %s malformatted: %v", versionString, err)
	}

	// in every case that the minor and major version are out of sync,
	// we want to preform a {down,up}grade. The common case here is newVersion > oldVersion,
	// but even in the opposite case, images are out of date and could be wiped
	return newVersion.Major != oldVersion.Major || newVersion.Minor != oldVersion.Minor, nil
}

// WriteVersionFile writes the version information to a given file
// file is the location of the old version file
// gitCommit is the current git commit version. It will be added to the file
// to aid in debugging, but will not be used to compare versions
func WriteVersionFile(file string) error {
	return writeVersionFile(file, gitCommit, Version)
}

// LogVersion logs the version and git information of this build
func LogVersion() {
	logrus.Infof("Starting CRI-O, version: %s, git: %v(%s)", Version, gitCommit, gitTreeState)
}

// writeVersionFile is an internal function for testing purposes
func writeVersionFile(file, gitCommit, version string) error {
	current, err := parseVersionConstant(version, gitCommit)
	// Sanity check-this should never happen
	if err != nil {
		return err
	}
	j, err := current.MarshalJSON()
	// Sanity check-this should never happen
	if err != nil {
		return err
	}

	// Create the top level directory if it doesn't exist
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}

	return renameio.WriteFile(file, j, 0o644)
}

// parseVersionConstant parses the Version variable above
// a const crioVersion would be kept, but golang doesn't support
// const structs. We will instead spend some runtime on CRI-O startup
// Because the version string doesn't keep track of the git commit,
// but it could be useful for debugging, we pass it in here
// If our version constant is properly formatted, this should never error
func parseVersionConstant(versionString, gitCommit string) (*semver.Version, error) {
	v, err := semver.Make(versionString)
	if err != nil {
		return nil, err
	}
	if gitCommit != "" {
		gitBuild, err := semver.NewBuildVersion(strings.Trim(gitCommit, "\""))
		// If gitCommit is empty, silently error, as it's helpful, but not needed.
		if err == nil {
			v.Build = append(v.Build, gitBuild)
		}
	}
	return &v, nil
}

func Get() *Info {
	return &Info{
		Version:      Version,
		GitCommit:    gitCommit,
		GitTreeState: gitTreeState,
		BuildDate:    buildDate,
		GoVersion:    runtime.Version(),
		Compiler:     runtime.Compiler,
		Platform:     fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Linkmode:     getLinkmode(),
	}
}

// String returns the string representation of the version info
func (i *Info) String() string {
	b := strings.Builder{}
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)

	v := reflect.ValueOf(*i)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		value := v.FieldByName(field.Name).String()
		if value != "" {
			fmt.Fprintf(w, "%s:\t%s", field.Name, value)
			if i+1 < t.NumField() {
				fmt.Fprintf(w, "\n")
			}
		}
	}

	w.Flush()
	return b.String()
}

func getLinkmode() string {
	abspath, err := os.Executable()
	if err != nil {
		logrus.Warnf("Encountered error finding binary to detect link mode: %v", err)
		return ""
	}

	if _, err = exec.LookPath("ldd"); err != nil {
		return ""
	}
	if _, err = utils.ExecCmd("ldd", abspath); err != nil {
		if strings.Contains(err.Error(), "not a dynamic executable") ||
			strings.Contains(strings.ToLower(err.Error()), "not a valid dynamic program") {
			return "static"
		}
		logrus.Warnf("Encountered error detecting link mode of binary: %v", err)
		return ""
	}

	return "dynamic"
}

// JSONString returns the JSON representation of the version info
func (i *Info) JSONString() (string, error) {
	b, err := json.MarshalIndent(i, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}
