package httpapi

import (
	"github.com/gofiber/fiber/v3"

	"github.com/FlameInTheDark/servy/internal/authz"
	"github.com/FlameInTheDark/servy/internal/creation"
	"github.com/FlameInTheDark/servy/internal/repo/gen"
)

// creationRequest is the API wizard payload (JSON tags) translated into the
// creation service request.
type creationRequest struct {
	CreateType       string  `json:"create_type"`
	Name             string  `json:"name"`
	ServerType       string  `json:"type"`
	Port             int     `json:"port"`
	ArchivePath      string  `json:"archive_path"`
	Executable       string  `json:"executable"`
	ExecutionCommand string  `json:"execution_command"`
	StopCommand      string  `json:"stop_command"`
	MinMem           string  `json:"min_mem"`
	MaxMem           string  `json:"max_mem"`
	JarURL           string  `json:"jar_url"`
	JarName          string  `json:"jar_name"`
	SteamSlug        string  `json:"steam_slug"`
	AppID            string  `json:"app_id"`
	// execution environment (process | docker)
	JavaSelection    string            `json:"java_selection"`
	Shell            string            `json:"shell"`
	ExecutionBackend string            `json:"execution_backend"`
	DockerImage      string            `json:"docker_image"`
	DockerEnv        map[string]string `json:"docker_env"`
	DockerPorts      []string          `json:"docker_ports"`
	DockerBinds      []string          `json:"docker_binds"`
	DockerNetwork    string            `json:"docker_network"`
	DockerWorkdir    string            `json:"docker_workdir"`
	DockerAutoremove bool              `json:"docker_autoremove"`
	Roles            []int64 `json:"roles"`
	AutoStart        bool    `json:"auto_start"`
	CrashDetection   bool    `json:"crash_detection"`
	Node             string  `json:"node"`
	DefinitionURL    string  `json:"definition_url"` // manifest auto-setup
}

func (r creationRequest) toCreationRequest() creation.Request {
	return creation.Request{
		CreateType: r.CreateType, Name: r.Name, ServerType: r.ServerType, Port: r.Port,
		ArchivePath: r.ArchivePath, Executable: r.Executable, ExecutionCommand: r.ExecutionCommand,
		StopCommand: r.StopCommand, MinMem: r.MinMem, MaxMem: r.MaxMem, JarURL: r.JarURL,
		JarName: r.JarName, SteamSlug: r.SteamSlug, AppID: r.AppID, Roles: r.Roles,
		AutoStart: r.AutoStart, CrashDetection: r.CrashDetection,
		JavaSelection: r.JavaSelection, Shell: r.Shell, ExecutionBackend: r.ExecutionBackend,
		DockerImage: r.DockerImage, DockerEnv: r.DockerEnv, DockerPorts: r.DockerPorts,
		DockerBinds: r.DockerBinds, DockerNetwork: r.DockerNetwork,
		DockerWorkdir: r.DockerWorkdir, DockerAutoremove: r.DockerAutoremove,
	}
}

// toDef builds a DB definition (used for remote node creation).
func (r creationRequest) toDef(serversRoot string) gen.Server {
	return gen.Server{
		ServerName: r.Name, ServerPort: int64(r.Port), Type: r.ServerType,
		Executable: r.Executable, ExecutionCommand: r.ExecutionCommand, StopCommand: r.StopCommand,
		AutoStart: bool2i(r.AutoStart), CrashDetection: bool2i(r.CrashDetection),
		JavaSelection: r.JavaSelection, Shell: r.Shell, ExecutionBackend: r.ExecutionBackend,
		DockerImage: &r.DockerImage, DockerNetwork: &r.DockerNetwork, DockerWorkdir: &r.DockerWorkdir,
		DockerAutoremove: bool2i(r.DockerAutoremove),
	}
}

func bool2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// currentUserFrom pulls the principal back from locals.
func currentUserFrom(c fiber.Ctx) *authz.Principal {
	if p, ok := c.Locals("principal").(*authz.Principal); ok {
		return p
	}
	return nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
