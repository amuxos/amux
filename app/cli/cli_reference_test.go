package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRenderCLIReferenceUsesLivePublicCommandTree(t *testing.T) {
	got := string(renderCLIReference(newRootCmdForPlatform(allCLIPlatforms)))

	for _, want := range []string{
		"# amux CLI 命令参考",
		"## `amux session run`",
		"`-n, --name`",
		"## `amux isolation helper install`",
		"## `amux isolation helper install`\n\nExplicitly install and start the root helper systemd unit\n\n**适用平台：** `linux`",
		"## `amux completion bash`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated reference does not contain %q", want)
		}
	}
	for _, hidden := range []string{
		"## `amux bridge`",
		"## `amux update`",
		"## `amux agent local`",
		"## `amux isolation helper drain`",
	} {
		if strings.Contains(got, hidden) {
			t.Fatalf("generated reference exposes hidden command %q", hidden)
		}
	}
}

func TestRenderCLIReferenceIncludesEveryPublicCommandExactlyOnce(t *testing.T) {
	root := newRootCmd()
	root.InitDefaultHelpCmd()
	got := string(renderCLIReference(root))

	var walk func(*cobra.Command, bool)
	walk = func(cmd *cobra.Command, parentVisible bool) {
		visible := parentVisible && !cmd.Hidden
		if visible {
			heading := "## `" + cmd.CommandPath() + "`"
			if count := strings.Count(got, heading+"\n"); count != 1 {
				t.Errorf("command %q has %d generated sections, want 1", cmd.CommandPath(), count)
			}
		}
		for _, child := range cmd.Commands() {
			walk(child, visible)
		}
	}
	walk(root, true)
}

func TestCLIReferencePlatformTreeIsDeterministic(t *testing.T) {
	allPlatforms := renderCLIReference(newRootCmdForPlatform(allCLIPlatforms))
	linux := renderCLIReference(newRootCmdForPlatform("linux"))
	darwin := renderCLIReference(newRootCmdForPlatform("darwin"))

	if !bytes.Equal(allPlatforms, linux) {
		t.Fatal("all-platform CLI reference must include the complete Linux command tree")
	}
	if bytes.Contains(darwin, []byte("## `amux isolation helper`")) {
		t.Fatal("Darwin runtime command tree includes the Linux-only helper")
	}
	if !bytes.Contains(allPlatforms, []byte("## `amux isolation helper`")) {
		t.Fatal("all-platform CLI reference omits the Linux-only helper")
	}
}

func TestRenderCLIReferenceIsIndependentOfHostDefaults(t *testing.T) {
	t.Setenv("AMUX_HOME", "/tmp/amux-doc-a")
	t.Setenv("SUDO_UID", "1001")
	t.Setenv("SUDO_GID", "1002")
	first := renderCLIReference(newRootCmdForPlatform(allCLIPlatforms))

	t.Setenv("AMUX_HOME", "/tmp/amux-doc-b")
	t.Setenv("SUDO_UID", "2001")
	t.Setenv("SUDO_GID", "2002")
	second := renderCLIReference(newRootCmdForPlatform(allCLIPlatforms))

	if !bytes.Equal(first, second) {
		t.Fatal("generated CLI reference depends on host-specific defaults")
	}
}

func TestRenderCLIReferenceRendersCommandMetadata(t *testing.T) {
	root := &cobra.Command{Use: "tool", Short: "Manage tools"}
	child := &cobra.Command{
		Use:     "run <name>",
		Aliases: []string{"start"},
		Short:   "Run a tool",
		Long:    "Run one configured tool.",
		Example: "  tool run demo --count 2",
	}
	child.Flags().IntP("count", "n", 1, "number of runs")
	child.Flags().String("runtime-path", "/host/path", "path resolved from the current host")
	markFlagDefaultRuntimeDependent(child, "runtime-path")
	child.Flags().String("secret", "", "hidden implementation detail")
	if err := child.Flags().MarkHidden("secret"); err != nil {
		t.Fatal(err)
	}
	root.AddCommand(child)

	got := string(renderCLIReference(root))
	for _, want := range []string{
		"## `tool run`",
		"Run one configured tool.",
		"**别名：** `start`",
		"tool run demo --count 2",
		"`-n, --count`",
		"`int`",
		"`1`",
		"`runtime-dependent`",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated metadata does not contain %q", want)
		}
	}
	if strings.Contains(got, "--secret") {
		t.Fatal("generated metadata includes a hidden flag")
	}
}
