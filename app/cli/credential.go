package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"

	pb "github.com/amuxos/amux/internal/transport/pbgen"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var credentialEnvNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

var (
	credentialInputFD = func(r io.Reader) (int, bool) {
		f, ok := r.(*os.File)
		if !ok {
			return 0, false
		}
		return int(f.Fd()), true
	}
	credentialIsTerminal   = term.IsTerminal
	credentialReadPassword = term.ReadPassword
)

type cliCredentialFile struct {
	Kind  string            `json:"kind"`
	Env   map[string]string `json:"env,omitempty"`
	Files map[string]string `json:"files,omitempty"`
}

type credentialProfileMetadata struct {
	Profile   string
	Kind      string
	EnvNames  []string
	FileNames []string
	Path      string
}

type credentialStoreScope string

const (
	credentialScopeLegacyHost       credentialStoreScope = "legacy-host"
	credentialScopeCurrentPrincipal credentialStoreScope = "current-principal"
)

type credentialSetResult struct {
	Metadata credentialProfileMetadata
	Scope    credentialStoreScope
}

const agentCredentialCommandPath = "amux agent credential"

func newCredentialCommand(invocationPath string) *cobra.Command {
	c := &cobra.Command{
		Use:   "credential",
		Short: "Manage credential profiles",
	}
	c.AddCommand(
		newCredentialSetCmd(invocationPath),
		newCredentialSetupCmd(invocationPath),
		newCredentialShowCmd(),
		newCredentialListCmd(),
	)
	return c
}

func newCredentialSetupCmd(invocationPath string) *cobra.Command {
	var principalID string
	var fromLegacyHost, rotateEnrollment bool
	cmd := &cobra.Command{
		Use:   "setup <profile> <env-name>",
		Short: "Set up one credential for an identified Principal",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, envName := args[0], args[1]
			if err := validateCredentialProfileName(profile); err != nil {
				return err
			}
			if err := validateCredentialEnvName(envName); err != nil {
				return err
			}
			principalID = strings.TrimSpace(principalID)
			parsedPrincipalID, err := uuid.Parse(principalID)
			if err != nil {
				return fmt.Errorf("principal must be a UUID: %w", err)
			}
			principalID = parsedPrincipalID.String()

			value, err := readCredentialSetupValue(
				cmd.InOrStdin(), cmd.ErrOrStderr(), profile, envName, principalID, fromLegacyHost,
			)
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("%s value must not be empty", envName)
			}

			if actorToken() == "" || rotateEnrollment {
				if _, err := createAndStorePrincipalEnrollment(cmd.Context(), principalEnrollmentOptions{
					Provider: "cli", Scope: "local", PrincipalID: principalID, Rotate: rotateEnrollment,
				}); err != nil {
					return err
				}
			}
			result, err := setCredentialValueForPrincipal(cmd.Context(), profile, envName, value, principalID)
			if err != nil {
				return err
			}
			if !containsCredentialEnv(result.Metadata.EnvNames, envName) {
				return fmt.Errorf("verify credential: profile %s does not define %s", profile, envName)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "credential ready for Principal %s (profile: %s, env: %s)\n", principalID, profile, envName)
			fmt.Fprintln(cmd.OutOrStdout(), "create the session again to use the credential")
			return nil
		},
	}
	cmd.Flags().StringVar(&principalID, "principal", "", "target Principal UUID")
	cmd.Flags().BoolVar(&fromLegacyHost, "from-legacy-host", false, "copy the value from the legacy-host credential store")
	cmd.Flags().BoolVar(&rotateEnrollment, "rotate-enrollment", false, "replace the local CLI enrollment with the target Principal")
	_ = cmd.MarkFlagRequired("principal")
	cmd.Example = fmt.Sprintf("%s setup openai-compat GLM_API_KEY --principal <principal-id>", invocationPath)
	return cmd
}

func readCredentialSetupValue(r io.Reader, promptOut io.Writer, profile, envName, principalID string, fromLegacyHost bool) (string, error) {
	legacyValue, hasLegacyValue, err := legacyCredentialEnvValue(profile, envName)
	if err != nil {
		return "", err
	}
	if fromLegacyHost {
		if !hasLegacyValue {
			return "", fmt.Errorf("legacy-host credential %s does not define %s", profile, envName)
		}
		return legacyValue, nil
	}
	if fd, ok := credentialInputFD(r); ok && credentialIsTerminal(fd) && hasLegacyValue {
		if _, err := fmt.Fprintf(promptOut, "Reuse existing legacy-host %s for Principal %s? [Y/n]: ", envName, principalID); err != nil {
			return "", err
		}
		answer, readErr := bufio.NewReader(r).ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return "", fmt.Errorf("read legacy-host reuse confirmation: %w", readErr)
		}
		switch strings.ToLower(strings.TrimSpace(answer)) {
		case "", "y", "yes":
			return legacyValue, nil
		case "n", "no":
			return readCredentialValue(r, promptOut, envName)
		default:
			return "", errors.New("answer must be yes or no")
		}
	}
	return readCredentialValue(r, promptOut, envName)
}

func legacyCredentialEnvValue(profile, envName string) (string, bool, error) {
	legacy, err := readExistingCredentialProfile(profile)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read legacy-host credential: %w", err)
	}
	value, ok := legacy.Env[envName]
	return value, ok && value != "", nil
}

func containsCredentialEnv(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func newCredentialSetCmd(invocationPath string) *cobra.Command {
	examples := fmt.Sprintf(strings.TrimSpace(`
%s set openai-compat OPENAI_API_KEY
printf '%%s\n' "$OPENAI_API_KEY" | %s set openai-compat OPENAI_API_KEY
`), invocationPath, invocationPath)
	if customize := distributionHooks.CredentialSetExamples; customize != nil {
		if extra := strings.TrimSpace(customize(invocationPath)); extra != "" {
			examples += "\n" + extra
		}
	}
	cmd := &cobra.Command{
		Use:               "set <profile> <env-name>",
		Short:             "Save one environment secret in a credential profile",
		Example:           examples,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile, envName := args[0], args[1]
			if err := validateCredentialProfileName(profile); err != nil {
				return err
			}
			if err := validateCredentialEnvName(envName); err != nil {
				return err
			}
			value, err := readCredentialValue(cmd.InOrStdin(), cmd.ErrOrStderr(), envName)
			if err != nil {
				return err
			}
			if value == "" {
				return fmt.Errorf("%s value must not be empty", envName)
			}
			result, err := setCredentialValue(cmd.Context(), profile, envName, value)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "saved credential profile %s (env: %s)\n", profile, envName)
			if result.Scope == credentialScopeCurrentPrincipal {
				fmt.Fprintln(cmd.OutOrStdout(), "scope: current Principal")
				return nil
			}
			path := strings.TrimSpace(result.Metadata.Path)
			if path == "" {
				path = credentialProfilePath(profile)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "scope: legacy-host (%s)\n", path)
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: identified Feishu sessions cannot use this credential; use the exact Principal ID shown by the Feishu error and run `%s setup %s %s --principal <principal-id> --from-legacy-host`\n", invocationPath, profile, envName)
			return nil
		},
	}
	return cmd
}

func credentialSetupCommand(invocationPath, profile, envName string) string {
	return fmt.Sprintf("%s set %s %s", strings.TrimSpace(invocationPath), strings.TrimSpace(profile), strings.TrimSpace(envName))
}

func newCredentialShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "show <profile>",
		Short:             "Show credential profile metadata without secret values",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profile := args[0]
			if err := validateCredentialProfileName(profile); err != nil {
				return err
			}
			meta, err := getCredentialMetadata(cmd.Context(), profile)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "profile: %s\n", profile)
			fmt.Fprintf(cmd.OutOrStdout(), "kind: %s\n", credentialKindOrDefault(meta.Kind))
			fmt.Fprintf(cmd.OutOrStdout(), "env: %s\n", strings.Join(meta.EnvNames, ", "))
			if len(meta.FileNames) > 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "files: %s\n", strings.Join(meta.FileNames, ", "))
			}
			return nil
		},
	}
}

func newCredentialListCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "ls",
		Aliases:           []string{"list"},
		Short:             "List credential profiles",
		Args:              cobra.NoArgs,
		ValidArgsFunction: completeNoFiles,
		RunE: func(cmd *cobra.Command, args []string) error {
			profiles, err := listCredentialMetadata(cmd.Context())
			if err != nil {
				return err
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "PROFILE\tKIND\tENV")
			for _, profile := range profiles {
				envKeys := strings.Join(profile.EnvNames, ",")
				if envKeys == "" {
					envKeys = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\n", profile.Profile, credentialKindOrDefault(profile.Kind), envKeys)
			}
			return w.Flush()
		},
	}
}

func readCredentialValue(r io.Reader, promptOut io.Writer, envName string) (string, error) {
	if fd, ok := credentialInputFD(r); ok && credentialIsTerminal(fd) {
		if _, err := fmt.Fprintf(promptOut, "Enter value for %s: ", envName); err != nil {
			return "", err
		}
		data, err := credentialReadPassword(fd)
		if _, newlineErr := fmt.Fprintln(promptOut); err == nil && newlineErr != nil {
			return "", newlineErr
		}
		if err != nil {
			return "", fmt.Errorf("read credential value: %w", err)
		}
		return strings.TrimRight(string(data), "\r\n"), nil
	}
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read credential value: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func validateCredentialProfileName(profile string) error {
	if !configProfileNamePattern.MatchString(profile) {
		return fmt.Errorf("credential profile must match %s", configProfileNamePattern.String())
	}
	return nil
}

func validateCredentialEnvName(name string) error {
	if !credentialEnvNamePattern.MatchString(name) {
		return fmt.Errorf("credential env name must match %s", credentialEnvNamePattern.String())
	}
	return nil
}

func credentialProfilePath(profile string) string {
	return filepath.Join(defaultAmuxDir(), "secrets", profile+".json")
}

func credentialMissingLocation(profile string) string {
	if actorToken() != "" {
		return "principal-scoped credential store"
	}
	return credentialProfilePath(profile)
}

func setCredentialValue(ctx context.Context, profile, envName, value string) (credentialSetResult, error) {
	return setCredentialValueForPrincipal(ctx, profile, envName, value, "")
}

func setCredentialValueForPrincipal(ctx context.Context, profile, envName, value, expectedPrincipalID string) (credentialSetResult, error) {
	if actorToken() != "" {
		hub, err := newHubClient(hubAddr())
		if err != nil {
			return credentialSetResult{}, err
		}
		defer hub.Close()
		meta, err := hub.SetCredential(ctx, profile, envName, value, expectedPrincipalID)
		if err != nil {
			return credentialSetResult{}, err
		}
		return credentialSetResult{Metadata: credentialMetadataFromProto(meta), Scope: credentialScopeCurrentPrincipal}, nil
	}
	cf, err := readCredentialProfile(profile)
	if err != nil {
		return credentialSetResult{}, err
	}
	if cf.Kind == "" {
		cf.Kind = "long-lived"
	}
	if cf.Env == nil {
		cf.Env = map[string]string{}
	}
	cf.Env[envName] = value
	if err := writeCredentialProfile(profile, cf); err != nil {
		return credentialSetResult{}, err
	}
	return credentialSetResult{Metadata: localCredentialMetadata(profile, cf), Scope: credentialScopeLegacyHost}, nil
}

func getCredentialMetadata(ctx context.Context, profile string) (credentialProfileMetadata, error) {
	if actorToken() != "" {
		hub, err := newHubClient(hubAddr())
		if err != nil {
			return credentialProfileMetadata{}, err
		}
		defer hub.Close()
		meta, err := hub.GetCredential(ctx, profile)
		if status.Code(err) == codes.NotFound {
			return credentialProfileMetadata{}, fmt.Errorf("%w: %v", os.ErrNotExist, err)
		}
		if err != nil {
			return credentialProfileMetadata{}, err
		}
		return credentialMetadataFromProto(meta), nil
	}
	cf, err := readExistingCredentialProfile(profile)
	if err != nil {
		return credentialProfileMetadata{}, err
	}
	return localCredentialMetadata(profile, cf), nil
}

func listCredentialMetadata(ctx context.Context) ([]credentialProfileMetadata, error) {
	if actorToken() != "" {
		hub, err := newHubClient(hubAddr())
		if err != nil {
			return nil, err
		}
		defer hub.Close()
		profiles, err := hub.ListCredentials(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]credentialProfileMetadata, 0, len(profiles))
		for _, profile := range profiles {
			out = append(out, credentialMetadataFromProto(profile))
		}
		return out, nil
	}
	profiles, err := listCredentialProfiles()
	if err != nil {
		return nil, err
	}
	out := make([]credentialProfileMetadata, 0, len(profiles))
	for _, profile := range profiles {
		cf, err := readExistingCredentialProfile(profile)
		if err != nil {
			return nil, err
		}
		out = append(out, localCredentialMetadata(profile, cf))
	}
	return out, nil
}

func localCredentialMetadata(profile string, cf cliCredentialFile) credentialProfileMetadata {
	return credentialProfileMetadata{
		Profile:   profile,
		Kind:      cf.Kind,
		EnvNames:  sortedCredentialKeys(cf.Env),
		FileNames: sortedCredentialKeys(cf.Files),
		Path:      credentialProfilePath(profile),
	}
}

func credentialMetadataFromProto(meta *pb.CredentialProfileMetadata) credentialProfileMetadata {
	if meta == nil {
		return credentialProfileMetadata{}
	}
	return credentialProfileMetadata{
		Profile:   meta.Profile,
		Kind:      meta.Kind,
		EnvNames:  append([]string(nil), meta.EnvNames...),
		FileNames: append([]string(nil), meta.FileNames...),
		Path:      meta.Path,
	}
}

func readCredentialProfile(profile string) (cliCredentialFile, error) {
	cf, err := readExistingCredentialProfile(profile)
	if err != nil {
		if os.IsNotExist(err) {
			return cliCredentialFile{}, nil
		}
		return cliCredentialFile{}, err
	}
	return cf, nil
}

func readExistingCredentialProfile(profile string) (cliCredentialFile, error) {
	data, err := os.ReadFile(credentialProfilePath(profile))
	if err != nil {
		return cliCredentialFile{}, err
	}
	var cf cliCredentialFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return cliCredentialFile{}, fmt.Errorf("parse credential profile %s: %w", profile, err)
	}
	return cf, nil
}

func writeCredentialProfile(profile string, cf cliCredentialFile) error {
	path := credentialProfilePath(profile)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		return err
	}
	return os.Chmod(path, 0600)
}

func listCredentialProfiles() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(defaultAmuxDir(), "secrets"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	profiles := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		profile := strings.TrimSuffix(entry.Name(), ".json")
		if !configProfileNamePattern.MatchString(profile) {
			continue
		}
		cf, err := readExistingCredentialProfile(profile)
		if err != nil {
			return nil, err
		}
		if cf.Kind != "" || len(cf.Env) > 0 || len(cf.Files) > 0 {
			profiles = append(profiles, profile)
		}
	}
	sort.Strings(profiles)
	return profiles, nil
}

func sortedCredentialKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func credentialKindOrDefault(kind string) string {
	if kind == "" {
		return "long-lived"
	}
	return kind
}
