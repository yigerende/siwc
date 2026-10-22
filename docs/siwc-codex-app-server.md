# Codex app-server

> For the complete documentation index, see [llms.txt](/llms.txt). Markdown versions of documentation pages are available by appending `.md` to the page URL.

## Using Codex app-server

If your app uses Codex app-server, configure it to send inference requests to the Responses API using an OAuth access token authorized for the user’s ChatGPT plan. You can use `model/list` to populate a model selector, but with the provider configuration below it can return a bundled client catalog. Treat it as a catalog, not an entitlement check; a successfully completed inference turn verifies access to the selected model for that request.

1. **Pass the OAuth access token to the child process.** After exchanging the authorization code, read the token response’s `access_token` field. Set `ACCESS_TOKEN` in the app-server child process’s environment to that value.
2. **Start app-server with a Responses provider.**

```bash
   codex app-server --listen stdio:// \
     -c 'model_provider="openai_chatgpt_plan"' \
     -c 'model_providers.openai_chatgpt_plan.name="ChatGPT plan"' \
     -c 'model_providers.openai_chatgpt_plan.base_url="https://api.openai.com/v1"' \
     -c 'model_providers.openai_chatgpt_plan.env_key="ACCESS_TOKEN"' \
     -c 'model_providers.openai_chatgpt_plan.wire_api="responses"' \
     -c 'model_providers.openai_chatgpt_plan.requires_openai_auth=false' \
     -c 'model_providers.openai_chatgpt_plan.supports_websockets=false'
```

   Codex sends the user’s OAuth access token as `Authorization: Bearer <access_token>` on requests to `/v1/responses`. No separate Codex sign-in is required.

3. **Drive the conversation over stdin/stdout.** Send newline-delimited JSON messages. Start with `initialize` and include these fields in `params.clientInfo`:
   - `name`: A stable identifier for your app, such as `my_app`. Codex uses this as the request originator for attribution. Use the same name across installations.
   - `title`: Your app’s human-readable name, such as `My App`.
   - `version`: Your app’s version, such as `1.2.3`. Codex includes it with the app identifier in the User-Agent.

   These fields identify the calling app. The name should match the `agent_name_hint` that your app sends as part of new user registration flow. Wait for `initialize` to succeed, then send `initialized`. Send `thread/start` with your selected model and save `result.thread.id`. Send `turn/start` with that `threadId` and the user’s message. Display `item/agentMessage/delta` events. When `turn/completed` arrives, check `turn.status`: only `completed` indicates success; `failed` and `interrupted` do not.

4. **Manage token renewal in your app.** Obtain a replacement access token using the flow in [Refreshing tokens](https://developers.openai.com/siwc/token-sharing-open-source/profiles-and-sessions#refreshing-tokens). Restart app-server with the updated `ACCESS_TOKEN`, initialize the new process, and resume the conversation using `thread/resume` with the saved thread ID.

Your app should read its own token file and supply the access token to the child process.