# Allow Doomer through Claude's network settings

Doomer needs an outbound HTTPS connection to its configured service to set up
reviews and check results. Approved reviews also upload repository snapshots,
task context, and finding responses. Network access and repository upload
approval are separate decisions.

## When onboarding cannot connect

The CLI reports `network_access` with a `required_host`, a settings link, this
guide, and a `retry_command`. Ask your agent to show the hostname and links.
A connection failure or an HTTP 403 can also have a service or authentication
cause. These details identify the host to check; they do not prove a network
policy blocked it.

1. Copy the exact `required_host` from the failed command. For production API
   requests this is normally `doomer.ai`. A development installation might use
   `doomer-marc.ngrok.app`. Allow the host your installation actually reports.
   Enter a hostname only, without `https://`, paths, or query strings.
2. Open Claude settings in the account and organization running your task.
3. For a managed organization, an **organization owner** opens **Organization
   settings** and finds the **Network access**, **Allow network egress**, or
   **Allowed domains** controls. Depending on the Claude surface and version,
   these may appear under **Code** or **Capabilities**. For a personal account,
   look in your own settings instead. A member who cannot edit organization
   controls needs the owner to make this change.
4. Select the option that permits specific domains and add the exact hostname
   to the existing list. Preserve domains your organization already allows.
   Review and save the change. A wildcard such as `*.ngrok.app` grants access
   to more hosts than this installation needs.
5. Return to your agent and say you have saved the setting. Ask it to run the
   returned `retry_command`. This rechecks the connection using the same Doomer
   task and preserves the task's setup progress.

Doomer can guide you to settings, but the owner makes the allowlist change.
Claude does not document a public API for changing this organization allowlist.
An agent should explain the requested access and wait for your decision.

## Claude Code cloud environments

Cloud coding sessions have their own environment network controls. Open the
menu beside the session title and select **Edit cloud environment**, or edit the
chosen cloud environment from the environment selector before starting a
session. Under **Network access**, choose **Custom**, add the exact hostname to
**Allowed domains**, and retain any existing domains and package-manager access
you need. An owner manages organization-shared environments.

These controls apply to the selected environment. Check that your task uses
the environment you edited.

[Claude cloud environment documentation](https://code.claude.com/docs/en/cloud-environments)

## If the connection still fails

- Check that the change was saved in the active account, organization, and
  environment. Confirm the reported hostname matches the saved entry.
- An existing session may retain its original network policy. If one retry
  still fails, start a new Claude session with the same repository and ask the
  agent to resume the existing Doomer task with `doomer prepare --task HANDLE`.
  Use the task handle from the earlier output. There is no need to create a new
  Doomer task or grant upload approval again if the existing grant remains valid.
- Each stage can contact a different host. Downloads may require
  `downloads.doomer.ai`; snapshot uploads may use a storage hostname. Review each
  newly reported hostname before allowing it. Keep signed upload URLs private.
- If the host is already allowed, check service availability, proxy settings,
  and the safe error code. A 403 alone does not establish a network-policy cause.
  Avoid repeated unchanged requests while the cause remains unresolved.

After setup succeeds, continue the original task. For an interrupted review,
use the returned command for that operation. Finding-response recovery first
checks task status, then sends any pending responses using the same task.

[Claude network access documentation](https://support.claude.com/en/articles/12111783-create-and-edit-files-with-claude)
