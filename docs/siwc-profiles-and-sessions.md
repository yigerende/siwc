# Accounts and sessions

> For the complete documentation index, see [llms.txt](/llms.txt). Markdown versions of documentation pages are available by appending `.md` to the page URL.

## Managing ChatGPT accounts

Your app needs to manage multiple account registrations so users can choose an existing ChatGPT account or add another. Keep each registration’s basic account info, issued client ID, and credentials separate, even when two registrations have the same email address. Use distinct, stable labels and show the active ChatGPT account in the app’s account picker.

When signing in again to the same account on the same host, reuse the registration’s issued `client_id` and stable `ext_agent_host_id`. Signing out or switching back to a saved ChatGPT account does not create a new client. See [Store credentials in a local file](https://developers.openai.com/siwc/token-sharing-open-source/sign-in#5-store-credentials-in-a-local-file) for the credential record format and storage details.




## Switching ChatGPT accounts

Keep each registration’s issued client ID and credentials separate. Email helps users recognize an account but may be identical across account registrations. Use a distinct, stable label for each account registration, and keep its issued client ID associated with the validated ID-token identity.

**Reauthorization:** Use the same authorize endpoint with the selected account’s issued `client_id` and fresh `state`, `nonce`, and PKCE values. Omit your app’s `agent_name_hint` from reauthorization requests. Include this host’s stable `ext_agent_host_id`. Send a retained `id_token` as `id_token_hint`; a saved email may also be sent as `login_hint`. Under the [expected returning sign-in](https://developers.openai.com/siwc/token-sharing-open-source/sign-in#2-start-authorization), `id_token_hint` skips the unified session selector. It does not change the workspace bound to this client or replace validation of the newly issued ID token. Follow [Validate the ID token and ChatGPT plan permission](https://developers.openai.com/siwc/token-sharing-open-source/sign-in#4-validate-the-id-token-and-chatgpt-plan-permission) before replacing credentials.

Within your OSS app, you may choose to offer an account picker that allows the user to:

- **Continue with a saved ChatGPT account:** Repeat OAuth with that account’s issued `client_id` and fresh state, nonce, and PKCE values. Keep the same host ID. Include retained login hints.
- **Use a different ChatGPT account:** Select its existing registration or register a new one with `client_id=dynamic_agent_client`. Switching accounts does not require signing out of every other saved account.
- **Sign out:** Stop requests. Attempt [End the renewable session](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions#end-the-renewable-session) below before clearing the selected account’s access, refresh, and ID tokens. Retain its account/client mapping and this host’s ID for a later sign-in.

Display the active ChatGPT account in settings or the account menu. Complete and validate the selected account’s OAuth flow before making it active. Never overwrite another account registration’s credentials or combine one registration’s client ID with another registration’s tokens.

### End the renewable session

When signing out, revoke the renewable session using the `revocation_endpoint` from `https://auth.openai.com/.well-known/openid-configuration`. Send a form-encoded `POST` with `token=<REFRESH_TOKEN>`, `token_type_hint=refresh_token`, and the issued `client_id`. An empty HTTP `200` is the revocation success response, including for an already-invalid token. Stop using the tokens and clear them locally after revocation.

For a network failure or `5xx`, retry with backoff while the refresh token is still available. If you finish signing out locally without confirming revocation, clear the local tokens and tell the user that remote revocation was not confirmed. The user can disconnect the app in ChatGPT Settings. Revoking this session does not delete the registered client.

## Refreshing tokens

Use standard OAuth refresh near access-token expiry. POST form-encoded `grant_type=refresh_token`, the issued `client_id` saved with this token set (not `dynamic_agent_client`), the saved `refresh_token`, and `resource=https://api.openai.com/v1` to `https://auth.openai.com/api/accounts/oauth/token`; omit `scope` to retain the grant.

Store and use the latest replacement, and serialize refreshes for the same session so two processes do not race a rotating token.

See [Token lifetimes](https://developers.openai.com/siwc/token-sharing-open-source/token-reference#token-lifetimes) for expiry and renewal limits, and [Refresh errors](https://developers.openai.com/siwc/token-sharing-open-source/errors-and-recovery#refresh-errors) for recovery.

## Credential security

Keep access, refresh, and retained ID tokens in protected local or self-hosted runtime storage. Keep tokens out of browser storage, source control, logs, analytics, and support transcripts.

Never put access or refresh tokens in URLs. Send an ID token as `id_token_hint` only to the OpenAI authorization endpoint, and redact authorization URLs containing that hint from logs and diagnostics. A host ID is an opaque identifier, not a token or proof of identity.

If credentials are compromised, instruct the user to disconnect the app in ChatGPT Settings.

## Tracking usage

Link from your tool’s usage-tracking pages to [ChatGPT Settings → Usage](https://chatgpt.com/settings/usage), where users can review app usage and manage each app’s access to their ChatGPT plan and credits.



> Illustration: Example ChatGPT App limits settings for Vercel, Canva, and Figma. Each app has a Manage limits action that opens a dialog where users can allow the app to use their ChatGPT plan, set the app’s weekly usage limit from 10% to 100%, and choose whether it can use credits after reaching plan limits. Changes apply only to this interactive example.



For ChatGPT Plus users, the five-hour usage limit is shared across all apps where they use their ChatGPT plan, including private and open-source clients. Usage in one app contributes to the same five-hour usage total, and no app receives a separate allowance. The five-hour usage limit does not apply for Pro users.