package buildinfo

const (
	AgentProtocolVersion                 = 1
	MinimumSupportedAgentProtocolVersion = 1
	RecommendedAgentVersion              = "dev"
	ExpectedDatabaseSchemaVersion        = 167
	WebUIVersion                         = "dev"
	UpdateStatus                         = "manual"
	UpdateChannel                        = "development"
)

var (
	Version              = "dev"
	GitCommit            = "unknown"
	BuildDate            = "unknown"
	AgentInstallerSHA256 = "unknown"
	AgentBundleBaseURL   = ""
)

type Info struct {
	Version                       string
	GitCommit                     string
	BuildDate                     string
	AgentInstallerSHA256          string
	AgentBundleBaseURL            string
	AgentProtocolVersion          int
	MinimumAgentProtocolVersion   int
	RecommendedAgentVersion       string
	ExpectedDatabaseSchemaVersion int
	WebUIVersion                  string
	UpdateStatus                  string
	UpdateChannel                 string
	AutomaticUpdatesSupported     bool
}

func Current() Info {
	return Info{
		Version:                       valueOrDefault(Version, "dev"),
		GitCommit:                     valueOrDefault(GitCommit, "unknown"),
		BuildDate:                     valueOrDefault(BuildDate, "unknown"),
		AgentInstallerSHA256:          valueOrDefault(AgentInstallerSHA256, "unknown"),
		AgentBundleBaseURL:            AgentBundleBaseURL,
		AgentProtocolVersion:          AgentProtocolVersion,
		MinimumAgentProtocolVersion:   MinimumSupportedAgentProtocolVersion,
		RecommendedAgentVersion:       RecommendedAgentVersion,
		ExpectedDatabaseSchemaVersion: ExpectedDatabaseSchemaVersion,
		WebUIVersion:                  WebUIVersion,
		UpdateStatus:                  UpdateStatus,
		UpdateChannel:                 UpdateChannel,
		AutomaticUpdatesSupported:     false,
	}
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
