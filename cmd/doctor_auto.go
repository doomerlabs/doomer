package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	watching "github.com/doomerlabs/doomer/internal/watch"
	"github.com/spf13/cobra"
)

type doctorAutoTarget struct {
	agent, file, owner, executable, command, scopedCommand, origin string
}

var (
	errDoctorAutoHookConflict  = errors.New("existing Doomer hooks need manual attention in the agent profile")
	errDoctorAutoHooksDisabled = errors.New("hooks are disabled in this agent profile; check the agent’s hook settings")
)

func runDoctorAutoFix(c *cobra.Command, repo, agent, manager, browserOrigin, browserSession string, allowUnconfigured, allowClaudeReviewCommands bool) (resultErr error) {
	status, browserReceipt := "auto_fix_failed", ""
	var root, reportEndpoint string
	var checks []string
	var changes doctorRepairChanges
	if c.Flags().Changed("browser-session") {
		if !doctorAutoSessionCode.MatchString(browserSession) && !doctorAutoBrowserCode.MatchString(browserSession) {
			return fmt.Errorf("copy a fresh auto-fix command from Doomer Doctor")
		}
		var err error
		reportEndpoint, err = doctorBrowserEndpoint(browserOrigin, repo, allowUnconfigured)
		if err != nil {
			return err
		}
		var receipt struct {
			Received bool `json:"received"`
		}
		if err := postDoctorAuto(c.Context(), reportEndpoint, map[string]string{"action": "auto-checking", "code": browserSession}, &receipt); err != nil {
			return err
		}
		if !receipt.Received {
			return fmt.Errorf("Doomer Doctor did not confirm the start of the setup check")
		}
	}
	defer func() {
		if status == "auto_fix_failed" {
			switch {
			case errors.Is(resultErr, errDoctorAutoHookConflict):
				status = "auto_fix_hook_conflict"
			case errors.Is(resultErr, errDoctorAutoHooksDisabled):
				status = "auto_fix_hooks_disabled"
			}
		}
		if checks == nil {
			checks = doctorAutoSetupChecks(c, root, manager, agent)
			printDoctorAutoChecks(c, checks)
		}
		if reportEndpoint == "" {
			return
		}
		reportCtx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
		defer cancel()
		payload := map[string]any{"action": "submit", "code": browserSession, "result": map[string]any{
			"source": "native", "checks": append([]string{status}, checks...),
		}}
		if browserReceipt != "" {
			payload["autoReceipt"] = browserReceipt
			if len(changes) > 0 {
				payload["result"].(map[string]any)["changes"] = changes
			}
		}
		var receipt struct {
			Received bool `json:"received"`
		}
		reportErr := postDoctorAuto(reportCtx, reportEndpoint, payload, &receipt)
		if reportErr == nil && !receipt.Received {
			reportErr = fmt.Errorf("Doomer Doctor did not confirm the browser result")
		}
		if reportErr != nil {
			fmt.Fprintln(c.ErrOrStderr(), "Could not send the auto-fix outcome to this browser.")
			if resultErr == nil {
				resultErr = reportErr
			}
		}
	}()

	ctx, cancel := context.WithTimeout(c.Context(), time.Minute)
	defer cancel()
	var err error
	root, err = watching.Root(ctx, repo)
	if err != nil {
		return err
	}
	if manager == "" {
		manager, err = defaultManagerHome()
	} else {
		manager, err = filepath.Abs(manager)
	}
	if err != nil {
		return err
	}
	target, err := loadDoctorAutoTarget(root, manager, agent)
	if err != nil {
		return err
	}
	agent = target.agent
	if browserOrigin == "" {
		browserOrigin = target.origin
	}
	if browserOrigin != target.origin && !allowUnconfigured {
		return fmt.Errorf("--browser-origin must match the configured Doomer service")
	}
	endpoint, err := doctorBrowserEndpoint(browserOrigin, root, true)
	if err != nil {
		return err
	}

	config, _, err := readDoctorAutoHookSettings(target.file)
	if err != nil {
		return err
	}
	hooks, err := npmValidateHooks(config)
	if err != nil {
		return err
	}
	hookCommand, missing, err := target.inspectHooks(config, hooks)
	if err != nil {
		return err
	}
	finding, allowedRepair := target.agent+"_hooks_installed", "none"
	if missing {
		finding, allowedRepair = target.agent+"_hooks_missing", "install_"+target.agent+"_hooks"
	}
	checks = doctorAutoSetupChecks(c, root, manager, agent)
	printDoctorAutoChecks(c, checks)
	if missing {
		fmt.Fprintln(c.OutOrStdout(), "Hooks: a missing Doomer hook can be repaired.")
	} else {
		fmt.Fprintln(c.OutOrStdout(), "Hooks: the expected Doomer hooks are configured.")
	}
	var plan struct {
		Code   string `json:"code"`
		Repair string `json:"repair"`
	}
	start := map[string]any{"action": "auto-start", "result": map[string]any{
		"source": "native", "checks": []string{"hook_configuration_checked", finding},
	}}
	if browserSession != "" {
		start["browserCode"] = browserSession
		start["diagnosis"] = append([]string{finding}, checks...)
	}
	if err := postDoctorAuto(ctx, endpoint, start, &plan); err != nil {
		return err
	}
	if doctorAutoSessionCode.MatchString(plan.Code) {
		browserReceipt = plan.Code
	}
	if browserReceipt == "" || (plan.Repair != "none" && plan.Repair != allowedRepair) {
		return fmt.Errorf("Doomer Doctor returned an unsupported repair for this checkout")
	}
	fmt.Fprintln(c.OutOrStdout(), "Connected to Doomer Doctor.")
	outcome := "unchanged"
	var repairErr error
	if plan.Repair != "none" {
		fmt.Fprintf(c.OutOrStdout(), "Repairing missing Doomer hooks for %s.\n", target.agent)
		repaired, err := applyDoctorAutoFix(ctx, target, root, manager, hookCommand, &changes)
		repairErr = err
		if err != nil {
			outcome = "failed"
			if repaired {
				status = "auto_fix_unverified"
			}
		} else if repaired {
			outcome = "repaired"
		}
	}
	var skillResult string
	var skillErr error
	if i := slices.Index(checks, "setup_skill_unavailable"); i >= 0 {
		skillResult, skillErr = restoreDoctorSkill(ctx, manager, target)
		if skillErr != nil {
			fmt.Fprintf(c.ErrOrStderr(), "Could not verify the skill repair: %v\n", skillErr)
			if skillResult == "" {
				skillResult = "setup_skill_restore_failed"
			}
		}
		if skillResult == "setup_skill_restored" || skillResult == "setup_skill_restore_unverified" {
			changes.skill(target)
		}
		if skillResult != "" {
			checks[i] = skillResult
			printDoctorAutoChecks(c, []string{skillResult})
		}
	}
	var permissionErr error
	permissionRepaired := false
	if agent == "codex" {
		if i := slices.Index(checks, "setup_agent_permissions_missing"); i >= 0 {
			checks[i], permissionErr = restoreDoctorCodexPermission(c, root, manager, target, &changes)
			permissionRepaired = checks[i] == "setup_agent_permissions_repaired"
			printDoctorAutoChecks(c, []string{checks[i]})
		}
	}
	if allowClaudeReviewCommands {
		for _, finding := range []string{"setup_agent_permissions_no_personal_rules", "setup_agent_permissions_incomplete", "setup_agent_permissions_missing"} {
			if i := slices.Index(checks, finding); i >= 0 {
				checks[i], permissionErr = repairDoctorClaudePermission(c, manager, target, &changes)
				permissionRepaired = checks[i] == "setup_agent_permissions_repaired"
				printDoctorAutoChecks(c, []string{checks[i]})
				break
			}
		}
	}
	var receipt struct {
		Received bool `json:"received"`
	}
	finish := map[string]string{"action": "auto-finish", "code": plan.Code, "outcome": outcome}
	reportErr := postDoctorAuto(ctx, endpoint, finish, &receipt)
	if reportErr == nil && !receipt.Received {
		reportErr = fmt.Errorf("Doomer Doctor did not confirm receipt")
	}
	if repairErr != nil {
		if reportErr != nil {
			fmt.Fprintln(c.ErrOrStderr(), reportErr)
		}
		return errors.Join(repairErr, skillErr, permissionErr)
	}
	switch {
	case outcome == "repaired":
		status = "auto_fix_repaired"
		fmt.Fprintln(c.OutOrStdout(), "Doomer hook files repaired and verified.")
	case missing && plan.Repair == "none":
		fmt.Fprintln(c.OutOrStdout(), "Doomer Doctor did not select a repair for the missing hooks.")
		if reportErr != nil {
			fmt.Fprintln(c.ErrOrStderr(), reportErr)
		}
		return fmt.Errorf("Doomer hooks still need attention")
	default:
		status = "auto_fix_unchanged"
		if !permissionRepaired && permissionErr == nil && skillErr == nil && skillResult != "setup_skill_restored" {
			fmt.Fprintln(c.OutOrStdout(), "Doctor made no local changes.")
		}
	}
	fmt.Fprintln(c.OutOrStdout(), "Open /hooks in your agent to check Doomer’s hook trust.")
	return errors.Join(reportErr, skillErr, permissionErr)
}

func loadDoctorAutoTarget(repo, manager, agent string) (doctorAutoTarget, error) {
	target := doctorAutoTarget{}
	if _, err := os.Lstat(filepath.Join(manager, "uninstalling.json")); !os.IsNotExist(err) {
		return target, fmt.Errorf("personal Doomer installer is unavailable")
	}
	state, err := readNPMSetupState(manager)
	if err != nil {
		return target, err
	}
	var agents, files []string
	var executable, api string
	for key, value := range map[string]any{"agents": &agents, "hookFiles": &files, "executable": &executable, "api": &api} {
		if err := state.get(key, value); err != nil {
			return target, fmt.Errorf("invalid installer %s: %w", key, err)
		}
	}
	if agent == "" && len(agents) == 1 {
		agent = agents[0]
	}
	if (agent != "claude" && agent != "codex") || !slices.Contains(agents, agent) {
		return target, fmt.Errorf("select an agent configured by this personal installation with --agent claude or --agent codex")
	}
	current, err := os.Stat(mustExecutable())
	if err != nil {
		return target, err
	}
	installed, err := os.Stat(executable)
	if err != nil || !filepath.IsAbs(executable) || !os.SameFile(current, installed) || isMDMRuntimePath(executable) {
		return target, fmt.Errorf("run --auto-fix with the CLI recorded by the selected personal installer")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return target, err
	}
	file, err := filepath.Abs(watching.HookFile(home, agent))
	if err != nil {
		return target, err
	}
	if !slices.Contains(files, file) {
		return target, fmt.Errorf("the active agent profile is outside this installer’s recorded hook files")
	}
	owner := "--owner=doomer-node"
	if filepath.Base(executable) == "doomer-dev" || filepath.Base(executable) == "doomer-dev.exe" {
		owner += "-dev"
	}
	command, err := npmHookCommand(npmSetupOptions{home: manager, executable: executable}, agent, owner)
	if err != nil {
		return target, err
	}
	environment, err := directoryWatchEnvironment(repo)
	if err != nil {
		return target, err
	}
	api, err = agentHookAPI("", environment.get("DOOMER_API_URL"), api)
	if err != nil {
		return target, err
	}
	service, err := url.Parse(api)
	if err != nil || service.Host == "" {
		return target, fmt.Errorf("invalid Doomer service URL")
	}
	target = doctorAutoTarget{agent: agent, file: file, owner: owner, executable: executable, command: command, origin: service.Scheme + "://" + service.Host}
	if owner == "--owner=doomer-node-dev" {
		wrapper := filepath.Join(home, ".doomer-developer", "doomer_hook_scope.py")
		info, wrapperErr := os.Stat(wrapper)
		if wrapperErr == nil && info.Mode().IsRegular() && doctorAutoPlainPath.MatchString(wrapper) && doctorAutoPlainPath.MatchString(executable) && doctorAutoPlainPath.MatchString(manager) {
			target.scopedCommand = fmt.Sprintf("/usr/bin/python3 %s development %s watch agent --agent=%s %s --manager-home=%s", wrapper, executable, agent, owner, manager)
		}
	}
	return target, nil
}

func applyDoctorAutoFix(ctx context.Context, target doctorAutoTarget, repo, manager, hookCommand string, changes *doctorRepairChanges) (bool, error) {
	unlock, err := npmSetupLock(filepath.Join(manager, "state.json"))
	if err != nil {
		return false, err
	}
	defer unlock()
	current, err := loadDoctorAutoTarget(repo, manager, target.agent)
	if err != nil {
		return false, err
	}
	if current != target {
		return false, fmt.Errorf("Doomer configuration changed during diagnosis; run Doctor again")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	applied, err := configureNPMHookFile(target.file, target.agent, target.owner, hookCommand, func(config, hooks npmJSONObject) (string, bool, error) {
		command, missing, err := target.inspectHooks(config, hooks)
		if err == nil && command != hookCommand {
			err = fmt.Errorf("Doomer hook routing changed during diagnosis; run Doctor again")
		}
		return command, missing, err
	}, changes.settings(target.file, func(config npmJSONObject) npmJSONObject {
		return doctorHookDiffSettings(config, hookCommand)
	}))
	if err != nil {
		return false, err
	}
	config, _, err := readDoctorAutoHookSettings(target.file)
	if err != nil {
		return applied, err
	}
	hooks, err := npmValidateHooks(config)
	if err != nil {
		return applied, err
	}
	command, missing, err := target.inspectHooks(config, hooks)
	if err == nil && (missing || command != hookCommand) {
		err = fmt.Errorf("Doomer could not verify the repaired hook files")
	}
	return applied, err
}

func readDoctorAutoHookSettings(file string) (npmJSONObject, []byte, error) {
	if info, err := os.Lstat(file); err == nil {
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil, nil, fmt.Errorf("auto-fix requires a regular agent settings file no larger than 4 MiB")
		}
	} else if !os.IsNotExist(err) {
		return nil, nil, err
	}
	return npmReadSettings(file)
}

func (target doctorAutoTarget) inspectHooks(config, hooks npmJSONObject) (string, bool, error) {
	if config["disableAllHooks"] == true {
		return "", false, errDoctorAutoHooksDisabled
	}
	command := target.command
	if target.scopedCommand != "" {
		command = target.scopedCommand
		// Keep valid direct hooks from older development installations. With
		// no surviving hooks, use the installed development scope wrapper.
	existing:
		for _, event := range npmHookEvents {
			groups, _ := npmObjects(hooks[event])
			for _, group := range groups {
				handlers, _ := npmObjects(group["hooks"])
				if slices.ContainsFunc(handlers, func(handler npmJSONObject) bool { return handler["command"] == target.command }) {
					command = target.command
					break existing
				}
			}
		}
	}
	scoped := command == target.scopedCommand
	missing := false
	for _, event := range npmHookEvents {
		expected := npmEventTimeout(target.agent, event) > 0
		count := 0
		groups, _ := npmObjects(hooks[event])
		for _, group := range groups {
			handlers, _ := npmObjects(group["hooks"])
			for _, handler := range handlers {
				value, _ := handler["command"].(string)
				doomerCommand := strings.Contains(strings.ToLower(value), "doomer") &&
					(strings.Contains(value, "watch hook") || strings.Contains(value, "watch agent") || strings.Contains(value, "agent-hook"))
				if !strings.Contains(value, "--owner=doomer-node") && !strings.Contains(value, "--managed-task-adapter") && !doomerCommand {
					continue
				}
				if scoped && !npmOwnsHook(handler, target.owner) && target.isSeparateProductionHook(value) {
					continue
				}
				matcher, _ := group["matcher"].(string)
				matcherOK := (matcher == "" || matcher == ".*") && event != "PermissionDenied" || matcher == "Bash" && event == "PermissionDenied"
				if !expected || count > 0 || !npmOwnsHook(handler, target.owner) || value != command || handler["type"] != "command" || handler["async"] == true || !matcherOK {
					return "", false, errDoctorAutoHookConflict
				}
				count++
			}
		}
		missing = missing || expected && count == 0
	}
	return command, missing, nil
}

// Recognize the production adapter that shares the development VM's profile.
// Its arguments are never used to generate a repair command.
func (target doctorAutoTarget) isSeparateProductionHook(command string) bool {
	args := strings.Fields(command)
	home, err := os.UserHomeDir()
	if err != nil || len(args) != 11 {
		return false
	}
	executable := filepath.Join(home, ".local", "bin", "doomer")
	return (args[0] == executable || args[0] == "'"+executable+"'") &&
		args[1] == "--profile" && (args[2] == "default" || args[2] == "'default'") && args[3] == "--api-url" &&
		slices.Equal(args[5:10], []string{"agent-hook", "--agent", target.agent, "--managed-task-adapter", "--state"})
}

var doctorAutoPlainPath = regexp.MustCompile(`^/[a-zA-Z0-9_./-]+$`)

var doctorAutoBrowserCode = regexp.MustCompile(`^[a-z]{2,8}(?:_[a-z]{2,8}){2}$`)

var doctorAutoSessionCode = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func postDoctorAuto(ctx context.Context, endpoint string, payload, response any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach Doomer Doctor: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("Doomer Doctor returned HTTP %d", res.StatusCode)
	}
	decoder := json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(response); err != nil {
		return fmt.Errorf("Doomer Doctor returned an invalid response")
	}
	return nil
}
