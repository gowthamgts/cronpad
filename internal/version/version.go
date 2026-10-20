// Package version contains build metadata set with linker flags.
package version

var Version = "dev"
var Commit = "unknown"
var BuildDate = "unknown"

type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"build_date"`
	Edition   string `json:"edition"`
}

func Get(edition string) Info {
	return Info{Version: Version, Commit: Commit, BuildDate: BuildDate, Edition: edition}
}
