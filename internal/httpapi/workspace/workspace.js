(() => {
  "use strict";

  const shell = document.querySelector("#app-shell");
  const inventoryList = document.querySelector("#inventory-list");
  const inventoryCount = document.querySelector("#inventory-count");
  const inventoryResults = document.querySelector("#inventory-results");
  const failedFilterCount = document.querySelector("#failed-filter-count");
  const workspaceLoading = document.querySelector("#workspace-loading");
  const workspaceContent = document.querySelector("#workspace-content");
  const diagnosisPane = document.querySelector("#diagnosis-pane");
  const diagnosisContent = document.querySelector("#diagnosis-content");
  const connectionState = document.querySelector("#connection-state");
  const connectionStateLabel = document.querySelector("#connection-state-label");
  const operatorScope = document.querySelector("#operator-scope");
  const scopeEnvironment = document.querySelector("#scope-environment");
  const scopeInstance = document.querySelector("#scope-instance");
  const scopeActor = document.querySelector("#scope-actor");
  const searchInput = document.querySelector("#automation-search");
  const globalSearch = document.querySelector("#global-search-control");
  const mobileSearchButton = document.querySelector("#mobile-search-button");
  const helpButton = document.querySelector("#help-button");
  const helpDialog = document.querySelector("#help-dialog");
  const connectionDialog = document.querySelector("#connection-dialog");
  const connectionForm = document.querySelector("#connection-form");
  const connectionError = document.querySelector("#connection-error");
  const connectionDescription = document.querySelector("#connection-description");
  const browserAuth = document.querySelector("#browser-auth");
  const signedOutAuth = document.querySelector("#signed-out-auth");
  const signedInAuth = document.querySelector("#signed-in-auth");
  const authIdentity = document.querySelector("#auth-identity");
  const oidcLoginButton = document.querySelector("#oidc-login-button");
  const oidcLogoutButton = document.querySelector("#oidc-logout-button");
  const tokenFallback = document.querySelector("#token-fallback");
  const tokenInput = document.querySelector("#management-token");
  const runDialog = document.querySelector("#run-dialog");
  const runForm = document.querySelector("#run-form");
  const runError = document.querySelector("#run-error");
  const runPayload = document.querySelector("#run-payload");
  const runIdempotency = document.querySelector("#run-idempotency");
  const runTargetAutomation = document.querySelector("#run-target-automation");
  const runTargetRevision = document.querySelector("#run-target-revision");
  const runTargetVersion = document.querySelector("#run-target-version");
  const runTargetLifecycle = document.querySelector("#run-target-lifecycle");
  const runTargetEnvironment = document.querySelector("#run-target-environment");
  const runTargetInstance = document.querySelector("#run-target-instance");
  const runTargetActor = document.querySelector("#run-target-actor");
  const actionDialog = document.querySelector("#action-dialog");
  const actionForm = document.querySelector("#action-form");
  const actionTitle = document.querySelector("#action-dialog-title");
  const actionMessage = document.querySelector("#action-dialog-message");
  const actionError = document.querySelector("#action-error");
  const actionSubmit = document.querySelector("#action-submit");
  const actionDismiss = document.querySelector("#action-dismiss");
  const actionIconUse = document.querySelector("#action-icon-use");
  const actionScopeEnvironment = document.querySelector("#action-scope-environment");
  const actionScopeInstance = document.querySelector("#action-scope-instance");
  const actionScopeActor = document.querySelector("#action-scope-actor");
  const approvalDialog = document.querySelector("#approval-dialog");
  const approvalForm = document.querySelector("#approval-form");
  const approvalTitle = document.querySelector("#approval-dialog-title");
  const approvalDescription = document.querySelector("#approval-dialog-description");
  const approvalScopeAutomation = document.querySelector("#approval-scope-automation");
  const approvalScopeRevision = document.querySelector("#approval-scope-revision");
  const approvalScopeRequested = document.querySelector("#approval-scope-requested");
  const approvalScopeExpires = document.querySelector("#approval-scope-expires");
  const approvalFields = document.querySelector("#approval-fields");
  const approvalActions = document.querySelector("#approval-actions");
  const approvalError = document.querySelector("#approval-error");
  const approvalNavCount = document.querySelector("#approval-nav-count");
  const receiptRegion = document.querySelector("#receipt-region");
  const toastRegion = document.querySelector("#toast-region");

  const HISTORY_LIMIT = 100;
  const views = new Set(["automations", "approvals", "runs", "deployments", "audit"]);
  const enabledFilters = new Set(["all", "enabled", "disabled", "failed"]);
  const runStatuses = new Set(["all", "queued", "running", "succeeded", "failed"]);
  const deploymentStatuses = new Set(["all", "in-progress", "succeeded", "failed"]);
  const approvalStatuses = new Set(["all", "pending", "approved", "rejected", "expired"]);
  let workspaceRequest = 0;
  let detailRequest = 0;
  let workspaceController = null;
  let detailController = null;
  let diagnosisRequest = 0;
  let diagnosisController = null;
  let detailRunsRequest = 0;
  let detailRunsController = null;
  let deploymentPollRequest = 0;
  let deploymentPollController = null;
  let runPollRequest = 0;
  let runPollController = null;
  let flowPollRequest = 0;
  let flowPollController = null;
  const feedRequests = {approvals: 0, runs: 0, deployments: 0, audit: 0};
  const feedControllers = {approvals: null, runs: null, deployments: null, audit: null};
  const diagnosisModalQuery = window.matchMedia("(max-width: 74rem)");

  class APIError extends Error {
    constructor(message, status, problem = {}) {
      super(message);
      this.status = status;
      this.code = problem.code || "request_failed";
      this.retryable = Boolean(problem.retryable);
      this.requestId = problem.requestId || "";
      this.context = problem.context || {};
    }
  }

  class AuthenticationRequired extends Error {}

  const state = {
    token: readToken(),
    auth: {configured: false, authenticated: false, identity: null, csrfToken: "", scope: {environment: "development", instance: location.host, actor: "connecting"}},
    automations: [],
    approvals: [],
    runs: [],
    deployments: [],
    audit: [],
    detail: null,
    detailRuns: [],
    detailApprovals: [],
    automationTab: "overview",
    flowLayer: "definition",
    flowRunID: "",
    flowRunDetails: {},
    flowRunLoading: {},
    flowRunErrors: {},
    flowEvidence: "revision",
    selectedAutomation: "",
    selectedRun: null,
    selectedDeployment: null,
    selectedAudit: null,
    diagnosisReturnFocus: null,
    diagnosisTab: "summary",
    view: "automations",
    enabledFilter: "all",
    query: "",
    runStatusFilter: "all",
    deploymentStatusFilter: "all",
    approvalStatusFilter: "pending",
    loading: true,
    mutating: false,
    pendingAction: null,
    pendingManualRun: null,
    selectedApproval: null,
    feedErrors: {approvals: "", runs: "", deployments: "", audit: ""},
    feedLoading: {approvals: true, runs: true, deployments: true, audit: true},
    feedNextCursor: {approvals: "", runs: "", deployments: "", audit: ""},
    detailRunsError: "",
    detailApprovalsError: "",
  };

  const icons = {
    schedule: "calendar",
    webhook: "webhook",
    email: "mail",
    ntfy: "radio",
    manual: "play",
  };

  function icon(name) {
    return `<svg class="icon" aria-hidden="true"><use href="#icon-${name}"></use></svg>`;
  }

  function escapeHTML(value) {
    return String(value ?? "").replace(/[&<>"']/g, (character) => ({
      "&": "&amp;",
      "<": "&lt;",
      ">": "&gt;",
      "\"": "&quot;",
      "'": "&#39;",
    })[character]);
  }

  function readRoute() {
    const params = new URLSearchParams(location.search);
    const view = params.get("view") || "automations";
    const enabled = params.get("enabled") || "all";
    const runStatus = params.get("runStatus") || "all";
    const deploymentStatus = params.get("deploymentStatus") || "all";
    const approvalStatus = params.get("approvalStatus") || "pending";
    let selectedAutomation = "";
    try {
      selectedAutomation = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
    } catch (_) {
      selectedAutomation = "";
    }
    return {
      view: views.has(view) ? view : "automations",
      enabledFilter: enabledFilters.has(enabled) ? enabled : "all",
      runStatusFilter: runStatuses.has(runStatus) ? runStatus : "all",
      deploymentStatusFilter: deploymentStatuses.has(deploymentStatus) ? deploymentStatus : "all",
      approvalStatusFilter: approvalStatuses.has(approvalStatus) ? approvalStatus : "pending",
      query: params.get("q") || "",
      selectedAutomation,
    };
  }

  function writeRoute(mode = "replace") {
    const url = new URL(location.href);
    const existing = new URLSearchParams(url.search);
    const params = new URLSearchParams();
    if (state.view !== "automations") params.set("view", state.view);
    if (state.enabledFilter !== "all") params.set("enabled", state.enabledFilter);
    if (state.runStatusFilter !== "all") params.set("runStatus", state.runStatusFilter);
    if (state.deploymentStatusFilter !== "all") params.set("deploymentStatus", state.deploymentStatusFilter);
    if (state.approvalStatusFilter !== "pending") params.set("approvalStatus", state.approvalStatusFilter);
    if (state.query) params.set("q", state.query);
    for (const key of ["run", "deployment", "audit", "tab"]) {
      if (existing.has(key)) params.set(key, existing.get(key));
    }
    url.search = params.toString();
    url.hash = state.selectedAutomation ? `/${encodeURIComponent(state.selectedAutomation)}` : "";
    history[mode === "push" ? "pushState" : "replaceState"](null, "", url);
  }

  function readDiagnosisRoute() {
    const params = new URLSearchParams(location.search);
    if (params.get("run")) return {kind: "run", id: params.get("run"), tab: params.get("tab") || "summary"};
    if (params.get("deployment")) return {kind: "deployment", id: params.get("deployment")};
    if (params.get("audit")) return {kind: "audit", id: params.get("audit")};
    return null;
  }

  function writeDiagnosisRoute(kind = "", id = "", mode = "replace") {
    const url = new URL(location.href);
    ["run", "deployment", "audit", "tab"].forEach((key) => url.searchParams.delete(key));
    if (kind && id) url.searchParams.set(kind, id);
    if (kind === "run" && state.diagnosisTab !== "summary") url.searchParams.set("tab", state.diagnosisTab);
    history[mode === "push" ? "pushState" : "replaceState"](null, "", url);
  }

  function openDiagnosisRoute() {
    const route = readDiagnosisRoute();
    if (!route) return;
    if (route.kind === "run") openRun(route.id, ["summary", "journey", "logs", "output"].includes(route.tab) ? route.tab : "summary", false);
    else if (route.kind === "deployment") openDeployment(route.id, false);
    else openAudit(route.id, false);
  }

  function applyRouteState() {
    const route = readRoute();
    const previousSelection = state.selectedAutomation;
    let canonicalized = false;
    if (route.selectedAutomation && !state.automations.some((item) => item.id === route.selectedAutomation)) {
      route.selectedAutomation = state.automations[0]?.id || "";
      canonicalized = true;
    }
    Object.assign(state, route);
    if (canonicalized) writeRoute();
    searchInput.value = state.query;
    closeDiagnosis({restoreFocus: false, syncRoute: false});
    if (state.selectedAutomation && state.selectedAutomation !== previousSelection && state.automations.some((item) => item.id === state.selectedAutomation)) {
      loadAutomation(state.selectedAutomation);
      window.setTimeout(openDiagnosisRoute, 0);
      return;
    }
    render();
    window.setTimeout(openDiagnosisRoute, 0);
  }

  function isAbort(error) {
    return error?.name === "AbortError";
  }

  function feedMessage(kind, error) {
    const labels = {approvals: "approval inbox", runs: "run history", deployments: "deployment history", audit: "audit history"};
    const retained = state[kind]?.length ? " Previous data remains visible." : "";
    return `${labels[kind]} could not be refreshed.${retained} ${recoveryGuidance(error)}`;
  }

  function detailRunsMessage(error) {
    const retained = state.detailRuns.length ? " Previous automation runs remain visible." : "";
    return `Run history for this automation could not be refreshed.${retained} ${recoveryGuidance(error)}`;
  }

  function detailApprovalsMessage(error) {
    const retained = state.detailApprovals.length ? " Previous approval evidence remains visible." : "";
    return `Approval evidence for this automation could not be refreshed.${retained} ${recoveryGuidance(error)}`;
  }

  function recoveryGuidance(error) {
    if (error instanceof APIError) {
      const request = error.requestId ? ` Request ID: ${error.requestId}.` : "";
      if (error.code === "insufficient_scope") return `This token does not grant the required ${error.context.requiredScope || "operation"} scope. Connect with an appropriate scoped token or an OIDC operator session.${request}`;
      if (error.code === "active_revision_changed") return `The active revision changed. Refresh the automation, review its new revision, and queue the run again.${request}`;
      if (error.code === "deployment_source_unavailable") return `The retained deployment source was pruned, so this package cannot be retried. Upload the intended package as a new deployment.${request}`;
      if (error.status === 404) return "The management endpoint or requested resource was not found. Verify this server exposes the current management API, then retry.";
      if (error.status === 409) return "The resource changed while the request was in progress. Refresh and review the current state before retrying.";
      if (error.status >= 500) return `The management API reported a server error. Verify the server is healthy, then retry.${request}`;
      return `The management API rejected the request. Review Connection settings and retry.${request}`;
    }
    if (error instanceof TypeError) return "Werkt could not reach the management API. Verify the server and network connection, then retry.";
    return "Retry the request. If it still fails, verify the server and Connection settings.";
  }

  function readToken() {
    try {
      return sessionStorage.getItem("werkt.managementToken") || "";
    } catch (_) {
      return "";
    }
  }

  function writeToken(value) {
    try {
      if (value) sessionStorage.setItem("werkt.managementToken", value);
      else sessionStorage.removeItem("werkt.managementToken");
    } catch (_) {
      // Private browsing policies may block storage; the in-memory token still works.
    }
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    headers.set("Accept", "application/json");
    if (state.token) {
      headers.set("Authorization", `Bearer ${state.token}`);
      headers.set("X-Werkt-Actor", "workspace:operator");
    } else if (!["GET", "HEAD", "OPTIONS"].includes((options.method || "GET").toUpperCase()) && state.auth.csrfToken) {
      headers.set("X-Werkt-CSRF", state.auth.csrfToken);
    }
    const {page = false, ...fetchOptions} = options;
    const response = await fetch(path, {...fetchOptions, headers});
    const contentType = response.headers.get("Content-Type") || "";
    const body = contentType.includes("application/json") ? await response.json() : null;
    if (response.status === 401) {
      setConnection("error", "Authentication required");
      state.auth.authenticated = false;
      state.auth.csrfToken = "";
      openConnection(state.auth.configured ? "Your session ended. Sign in again to continue." : "Enter the management token configured for this Werkt server.");
      throw new AuthenticationRequired();
    }
    if (!response.ok) {
      throw new APIError(body?.message || body?.error || `Request failed with status ${response.status}`, response.status, body || {});
    }
    if (page) return {items: body, nextCursor: response.headers.get("X-Werkt-Next-Cursor") || ""};
    return body;
  }

  async function refreshBrowserSession() {
    try {
      const response = await fetch("/api/v1/auth/session", {headers: {Accept: "application/json"}});
      if (!response.ok) return;
      const session = await response.json();
      state.auth = {
        configured: Boolean(session.configured),
        authenticated: Boolean(session.authenticated),
        identity: session.identity || null,
        csrfToken: session.csrfToken || "",
        scope: session.scope || {environment: "development", instance: location.host, actor: "unknown"},
      };
    } catch (_) {
      state.auth = {configured: false, authenticated: false, identity: null, csrfToken: "", scope: {environment: "development", instance: location.host, actor: "unavailable"}};
    }
    renderOperatorScope();
    renderConnectionAuth();
  }

  function currentScope() {
    const scope = state.auth.scope || {};
    return {
      environment: scope.environment || "development",
      instance: scope.instance || location.host,
      actor: state.token ? "workspace:operator" : (scope.actor || "unknown"),
    };
  }

  function renderOperatorScope() {
    const scope = currentScope();
    scopeEnvironment.textContent = scope.environment;
    scopeInstance.textContent = scope.instance;
    scopeActor.textContent = state.auth.authenticated ? identityLabel() : (state.token ? "Agent token" : "Local session");
    operatorScope.title = `${scope.environment} · ${scope.instance} · ${scope.actor}`;
  }

  function identityLabel() {
    const identity = state.auth.identity || {};
    return identity.name || identity.username || identity.email || "Authelia account";
  }

  function connectedLabel() {
    if (state.token) return "Connected · token";
    if (state.auth.authenticated) return `Connected · ${identityLabel()}`;
    return "Connected";
  }

  function renderConnectionAuth() {
    browserAuth.hidden = !state.auth.configured;
    signedOutAuth.hidden = state.auth.authenticated;
    signedInAuth.hidden = !state.auth.authenticated;
    authIdentity.textContent = identityLabel();
    connectionDescription.textContent = state.auth.configured
      ? (state.auth.authenticated ? "Your browser session is verified by Authelia and attributed in Werkt's audit trail." : "Sign in with Authelia for a secure, attributed browser session.")
      : "Connect with the management bearer token configured for this Werkt server.";
    tokenFallback.open = Boolean(state.token) || !state.auth.configured;
  }

  function setConnection(kind, label) {
    connectionState.className = `connection${kind ? ` is-${kind}` : ""}`;
    connectionStateLabel.textContent = label;
  }

  async function loadWorkspace({preserveSelection = true} = {}) {
    const request = ++workspaceRequest;
    workspaceController?.abort();
    workspaceController = new AbortController();
    for (const kind of Object.keys(feedControllers)) {
      feedRequests[kind] += 1;
      feedControllers[kind]?.abort();
      feedControllers[kind] = null;
    }
    const workspaceFeedRequests = {...feedRequests};
    const {signal} = workspaceController;
    const firstLoad = state.automations.length === 0;
    shell.classList.remove("has-fatal-error");
    state.loading = true;
    if (firstLoad) {
      inventoryCount.textContent = "Loading inventory…";
      setConnection("", "Connecting");
      workspaceLoading.hidden = false;
      workspaceContent.hidden = true;
    } else {
      setConnection("", "Refreshing");
    }
    try {
      const automations = await api("/api/v1/automations", {signal});
      if (request !== workspaceRequest) return;
      state.automations = automations;
      for (const kind of Object.keys(feedRequests)) {
        if (feedRequests[kind] !== workspaceFeedRequests[kind]) continue;
        state.feedErrors[kind] = "";
        state.feedLoading[kind] = true;
      }
      setConnection("connected", connectedLabel());

      const hashSelection = readRoute().selectedAutomation;
      const candidate = preserveSelection ? (state.selectedAutomation || hashSelection) : hashSelection;
      state.selectedAutomation = automations.some((item) => item.id === candidate)
        ? candidate
        : (preserveSelection && !candidate ? "" : (automations[0]?.id || ""));
      writeRoute();
      if (!state.selectedAutomation) {
        state.detail = null;
        state.detailRuns = [];
      }
      workspaceLoading.hidden = true;
      workspaceContent.hidden = false;
      render();

      const detailPromise = state.selectedAutomation
        ? loadAutomation(state.selectedAutomation, false).catch((error) => {
          if (!isAbort(error) && !(error instanceof AuthenticationRequired)) renderDetailError(error);
        })
        : Promise.resolve();
      const feeds = await Promise.allSettled([
        api(`/api/v1/approvals?limit=${HISTORY_LIMIT}`, {signal, page: true}),
        api(`/api/v1/runs?limit=${HISTORY_LIMIT}`, {signal, page: true}),
        api(`/api/v1/deployments?limit=${HISTORY_LIMIT}`, {signal, page: true}),
        api(`/api/v1/audit?limit=${HISTORY_LIMIT}`, {signal, page: true}),
      ]);
      if (request !== workspaceRequest) return;
      for (const [index, kind] of ["approvals", "runs", "deployments", "audit"].entries()) {
        if (feedRequests[kind] !== workspaceFeedRequests[kind]) continue;
        const result = feeds[index];
        state.feedLoading[kind] = false;
        if (result.status === "fulfilled") {
          state[kind] = result.value.items;
          state.feedNextCursor[kind] = result.value.nextCursor;
          state.feedErrors[kind] = "";
        } else if (!isAbort(result.reason)) {
          state.feedErrors[kind] = feedMessage(kind, result.reason);
        }
      }
      render();
      openDiagnosisRoute();
      await detailPromise;
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) renderFatalError(error);
    } finally {
      if (request !== workspaceRequest) return;
      state.loading = false;
      workspaceLoading.hidden = true;
      workspaceContent.hidden = false;
    }
  }

  async function loadAutomation(automationID, rerender = true) {
    const request = ++detailRequest;
    const runsRequest = ++detailRunsRequest;
    detailRunsController?.abort();
    detailController?.abort();
    detailController = new AbortController();
    const {signal} = detailController;
    const encoded = encodeURIComponent(automationID);
    const [detailResult, runsResult, approvalsResult] = await Promise.allSettled([
      api(`/api/v1/automations/${encoded}`, {signal}),
      api(`/api/v1/runs?automation=${encoded}&limit=${HISTORY_LIMIT}`, {signal, page: true}),
      api(`/api/v1/approvals?automation=${encoded}&limit=${HISTORY_LIMIT}`, {signal, page: true}),
    ]);
    if (request !== detailRequest || automationID !== state.selectedAutomation) return;
    if (detailResult.status === "rejected") throw detailResult.reason;
    state.detail = detailResult.value;
    if (runsRequest !== detailRunsRequest) {
      // A newer explicit run-history refresh owns this surface.
    } else if (runsResult.status === "fulfilled") {
      state.detailRuns = runsResult.value.items;
      state.detailRunsError = "";
    } else if (!isAbort(runsResult.reason)) {
      state.detailRunsError = detailRunsMessage(runsResult.reason);
    }
    if (approvalsResult.status === "fulfilled") {
      state.detailApprovals = approvalsResult.value.items;
      state.detailApprovalsError = "";
    } else if (!isAbort(approvalsResult.reason)) {
      state.detailApprovalsError = detailApprovalsMessage(approvalsResult.reason);
    }
    if (rerender || state.detail) render();
    if (state.automationTab === "flow" && state.flowLayer === "live" && state.flowRunID) loadFlowRun(state.flowRunID, {force: true});
  }

  async function selectAutomation(automationID) {
    if (!automationID || automationID === state.selectedAutomation) {
      shell.classList.add("has-selection");
      writeRoute("push");
      render();
      return;
    }
    state.selectedAutomation = automationID;
    state.detail = null;
    state.detailRuns = [];
    state.detailApprovals = [];
    state.automationTab = "overview";
    state.flowLayer = "definition";
    state.flowRunID = "";
    state.flowRunDetails = {};
    state.flowRunLoading = {};
    state.flowRunErrors = {};
    state.flowEvidence = "revision";
    state.detailApprovalsError = "";
    state.detailRunsError = "";
    shell.classList.add("has-selection");
    writeRoute("push");
    renderInventory();
    showDetailLoading();
    try {
      await loadAutomation(automationID);
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) renderDetailError(error);
    }
  }

  function render() {
    renderOperatorScope();
    renderNavigation();
    renderInventory();
    renderCurrentView();
    inventoryCount.textContent = `${state.automations.length} ${state.automations.length === 1 ? "automation" : "automations"}`;
  }

  function renderNavigation() {
    document.querySelectorAll("[data-view]").forEach((button) => {
      const active = button.dataset.view === state.view;
      button.classList.toggle("is-active", active);
      if (active) button.setAttribute("aria-current", "page");
      else button.removeAttribute("aria-current");
    });
    shell.classList.toggle("is-global-view", state.view !== "automations");
    shell.classList.toggle("has-selection", state.view === "automations" && Boolean(state.selectedAutomation));
    const pending = state.approvals.filter((approval) => approval.status === "pending").length;
    approvalNavCount.textContent = pending;
    approvalNavCount.hidden = pending === 0;
  }

  function automationHealth(automation) {
    const run = automation.latestRun;
    const paused = !automation.enabled;
    if (!run) {
      const label = paused ? "Paused · no runs yet" : "No runs yet";
      return {className: paused ? "is-paused" : "", label, htmlLabel: escapeHTML(label), needsAttention: false};
    }
    const when = relativeTime(run.createdAt);
    const suffix = paused ? " · Paused" : "";
    if (run.status === "failed") return {className: "is-failed", label: `Latest run failed · ${when}${suffix}`, htmlLabel: `Latest run failed · ${relativeTimeElement(run.createdAt)}${suffix}`, needsAttention: true};
    if (run.status === "running" || run.status === "queued") return {className: "is-running", label: `${capitalize(run.status)} · ${when}${suffix}`, htmlLabel: `${capitalize(run.status)} · ${relativeTimeElement(run.createdAt)}${suffix}`, needsAttention: false};
    const successLabel = paused ? "Paused · latest run succeeded" : "Latest run succeeded";
    return {className: paused ? "is-paused" : "is-healthy", label: `${successLabel} · ${when}`, htmlLabel: `${successLabel} · ${relativeTimeElement(run.createdAt)}`, needsAttention: false};
  }

  function filteredAutomations() {
    const query = state.query.toLocaleLowerCase();
    return state.automations.filter((automation) => {
      if (state.enabledFilter === "enabled" && !automation.enabled) return false;
      if (state.enabledFilter === "disabled" && automation.enabled) return false;
      if (state.enabledFilter === "failed" && !automationHealth(automation).needsAttention) return false;
      if (!query) return true;
      return [automation.id, automation.project, automation.folder, automation.description, ...(automation.labels || [])]
        .join(" ").toLocaleLowerCase().includes(query);
    });
  }

  function renderInventory() {
    document.querySelectorAll("[data-enabled-filter]").forEach((button) => {
      const active = button.dataset.enabledFilter === state.enabledFilter;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
    const failedCount = state.automations.filter((automation) => automationHealth(automation).needsAttention).length;
    failedFilterCount.textContent = failedCount;
    failedFilterCount.hidden = failedCount === 0;
    const automations = filteredAutomations();
    const projectCount = new Set(automations.map((automation) => automation.project)).size;
    inventoryResults.textContent = state.enabledFilter === "failed"
      ? `Showing ${automations.length} ${automations.length === 1 ? "automation" : "automations"} needing attention in ${projectCount} ${projectCount === 1 ? "project" : "projects"}.`
      : `Showing ${automations.length} of ${state.automations.length} automations.`;
    if (!automations.length) {
      inventoryList.innerHTML = state.automations.length
        ? `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("search")}</span><h2>No matching automations</h2><p>Adjust the search or lifecycle filter to restore the inventory.</p></div></div>`
        : `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("bolt")}</span><h2>No automations yet</h2><p>Deploy a package to create the first immutable revision.</p></div></div>`;
      return;
    }
    const projects = new Map();
    for (const automation of automations) {
      if (!projects.has(automation.project)) projects.set(automation.project, new Map());
      const folder = automation.folder || "Unfiled";
      if (!projects.get(automation.project).has(folder)) projects.get(automation.project).set(folder, []);
      projects.get(automation.project).get(folder).push(automation);
    }
    inventoryList.innerHTML = [...projects.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([project, folders]) => {
      const count = [...folders.values()].reduce((total, items) => total + items.length, 0);
      const failures = [...folders.values()].flat().filter((automation) => automationHealth(automation).needsAttention).length;
      const countLabel = failures ? `${failures} failed · ${count} total` : `${count} total`;
      return `<section class="inventory-group"><div class="inventory-group-title"><span>${escapeHTML(project)}</span><span class="${failures ? "has-failures" : ""}">${countLabel}</span></div>${[...folders.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([folder, items]) => `<div class="inventory-folder"><div class="inventory-folder-label">${escapeHTML(folder)}</div>${items.sort((a, b) => Number(automationHealth(b).needsAttention) - Number(automationHealth(a).needsAttention) || a.id.localeCompare(b.id)).map(renderInventoryItem).join("")}</div>`).join("")}</section>`;
    }).join("");
    syncInventoryRoving();
  }

  function syncInventoryRoving() {
    const items = [...inventoryList.querySelectorAll(".inventory-item")];
    const target = items.find((item) => item.dataset.automation === state.selectedAutomation) || items[0];
    items.forEach((item) => { item.tabIndex = item === target ? 0 : -1; });
  }

  function renderInventoryItem(automation) {
    const health = automationHealth(automation);
    const selected = automation.id === state.selectedAutomation;
    return `<button class="inventory-item${selected ? " is-selected" : ""}" type="button" data-automation="${escapeHTML(automation.id)}" ${selected ? 'aria-current="true"' : ""} aria-label="${escapeHTML(automation.id)}, ${escapeHTML(health.label)}" aria-keyshortcuts="ArrowUp ArrowDown Home End Enter" title="${escapeHTML(automation.description || automation.id)}">
      <span class="status-dot ${health.className}" aria-hidden="true"></span>
      <span><span class="inventory-name">${escapeHTML(automation.id)}</span><span class="inventory-meta ${health.className}">${health.htmlLabel}</span></span>
    </button>`;
  }

  function renderCurrentView() {
    if (state.view === "approvals") renderGlobalApprovals();
    else if (state.view === "runs") renderGlobalRuns();
    else if (state.view === "deployments") renderGlobalDeployments();
    else if (state.view === "audit") renderGlobalAudit();
    else if (!state.automations.length) renderEmptyWorkspace();
    else if (!state.selectedAutomation) renderSelectAutomation();
    else if (state.detail) renderAutomationDetail();
    else showDetailLoading();
  }

  function renderSelectAutomation() {
    workspaceContent.innerHTML = `<section class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("bolt")}</span><h2>Select an automation</h2><p>Choose an automation to see whether it is healthy, what happens next, and whether it needs you.</p></div></section>`;
  }

  function showDetailLoading() {
    workspaceContent.innerHTML = `<div class="workspace-loading" role="status"><span class="sr-only">Loading automation…</span><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-block"></div><div class="skeleton skeleton-block short"></div></div>`;
  }

  function renderEmptyWorkspace() {
    const command = "werkt deploy ./path/to/automation";
    workspaceContent.innerHTML = `<section class="empty-state onboarding-empty"><div class="empty-state-inner"><span class="empty-symbol">${icon("bolt")}</span><h2>Bring your first automation to life</h2><p>Werkt validates the package, connects its triggers, and keeps every run attributable and recoverable.</p><ol class="onboarding-flow"><li><strong>Automation</strong><span>Describe one bounded job.</span></li><li><strong>Trigger</strong><span>Choose what starts it.</span></li><li><strong>Run</strong><span>Inspect every outcome.</span></li></ol><div class="command-row"><code class="empty-command">${command}</code><button class="button button-quiet button-compact" type="button" data-copy-value="${command}" aria-label="Copy first deployment command">${icon("copy")}Copy</button></div><p class="onboarding-note">Nothing becomes active until validation and deployment succeed.</p></div></section>`;
  }

  function actorLabel(actor) {
    const value = String(actor || "");
    if (!value) return "Unknown operator";
    if (value.startsWith("workspace:oidc:")) return "Signed-in operator";
    if (value.startsWith("system:")) return "Werkt";
    if (value === "cli" || value.startsWith("cli:")) return "Werkt CLI";
    if (value.startsWith("workspace:")) return "Workspace operator";
    return value;
  }

  function nextEventSummary(triggers, automationEnabled) {
    if (!automationEnabled) return "Paused until resumed";
    const enabled = triggers.filter((trigger) => trigger.enabled);
    const scheduled = enabled.filter((trigger) => trigger.nextFireAt)
      .sort((left, right) => new Date(left.nextFireAt) - new Date(right.nextFireAt));
    if (scheduled.length) return `Scheduled ${relativeTimeElement(scheduled[0].nextFireAt)}`;
    if (enabled.length) return "Waiting for an event";
    return "No enabled trigger";
  }

  function executionProtection(execution) {
    const concurrency = {
      forbid: "One run at a time",
      replace: "Newest run takes over",
      allow: "Runs may overlap",
    }[execution.concurrency] || "Standard execution";
    return `${concurrency} · ${execution.timeout || "5m"} limit`;
  }

  function overviewRuns(runs) {
    const selected = [];
    for (const run of runs) {
      if (run.status === "succeeded") continue;
      selected.push(run);
      if (selected.length === 3) break;
    }
    for (const run of runs) {
      if (selected.length === 3) break;
      if (!selected.includes(run)) selected.push(run);
    }
    return selected.sort((left, right) => runs.indexOf(left) - runs.indexOf(right));
  }

  function runHistorySummary(runs) {
    if (!runs.length) return "No outcomes recorded yet";
    const succeeded = runs.filter((run) => run.status === "succeeded").length;
    const failed = runs.filter((run) => run.status === "failed").length;
    const active = runs.filter((run) => ["queued", "running"].includes(run.status)).length;
    const parts = [];
    if (failed) parts.push(`${failed} ${failed === 1 ? "failure" : "failures"} to review`);
    if (active) parts.push(`${active} in progress`);
    if (succeeded) parts.push(`${succeeded} successful`);
    return `${parts.join(" · ")} in loaded history`;
  }

  function copyValueButton(value, label) {
    if (!value) return "";
    return `<button class="copy-value" type="button" data-copy-value="${escapeHTML(value)}" aria-label="Copy ${escapeHTML(label)}" title="Copy ${escapeHTML(label)}">${icon("copy")}</button>`;
  }

  function technicalFact(label, value) {
    const display = value || "Not recorded";
    return `<div><dt>${escapeHTML(label)}</dt><dd><code>${escapeHTML(display)}</code>${value ? copyValueButton(value, label) : ""}</dd></div>`;
  }

  function renderAutomationTechnicalDetails(detail, activeRevision, runtime, execution) {
    return `<details class="technical-disclosure automation-technical"><summary>Technical details</summary><p>Exact immutable identifiers and execution settings for audits, support, and incident response.</p><dl class="technical-grid">${technicalFact("Revision", detail.activeRevisionId)}${technicalFact("Content SHA-256", activeRevision?.contentHash)}${technicalFact("Artifact SHA-256", activeRevision?.provenance?.artifactDigest)}${technicalFact("Runtime", runtime.image || runtime.language || "Local process")}<div><dt>Execution policy</dt><dd>${escapeHTML(execution.concurrency || "allow")} · ${escapeHTML(execution.timeout || "5m timeout")}</dd></div></dl></details>`;
  }

  function renderAutomationDetail() {
    const detail = state.detail;
    const runtime = detail.manifest?.runtime || {};
    const execution = detail.manifest?.execution || {};
    const activeRevision = detail.revisions?.find((revision) => revision.active) || detail.revisions?.[0];
    const triggers = detail.triggers || [];
    const recentRuns = overviewRuns(state.detailRuns);
    const status = detail.enabled ? "active" : "paused";
    const latestRun = detail.latestRun || state.detailRuns[0];
    const health = automationHealth({...detail, latestRun});
    workspaceContent.innerHTML = `<article class="detail-shell">
      <header class="detail-header">
        <button class="icon-button mobile-back" type="button" data-mobile-back aria-label="Back to automation inventory">${icon("arrow-left")}</button>
        <div class="breadcrumbs"><span>${escapeHTML(detail.project)}</span>${icon("chevron")}<span>${escapeHTML(detail.folder || "Unfiled")}</span>${icon("chevron")}<span>${escapeHTML(detail.id)}</span></div>
        <div class="detail-title-row">
          <div class="detail-title"><div class="detail-title-line"><h2>${escapeHTML(detail.id)}</h2><span class="status-badge status-${status}">${detail.enabled ? "Active" : "Paused"}</span></div><p>${escapeHTML(detail.description || "No description is set in this automation's manifest.")}</p></div>
          <div class="detail-actions">
            <button class="button button-quiet" type="button" data-toggle-enabled="${detail.enabled ? "false" : "true"}" ${state.mutating ? "disabled" : ""}>${icon(detail.enabled ? "pause" : "play")}${detail.enabled ? "Pause" : "Resume"}</button>
            <button class="button button-primary" type="button" data-open-run aria-keyshortcuts="R" ${state.mutating ? "disabled" : ""}>${icon("play")}Test run</button>
          </div>
        </div>
      </header>
      <nav class="detail-tabs" role="tablist" aria-label="Automation detail">
        <button id="automation-tab-overview" class="tab-button" type="button" role="tab" data-automation-tab="overview" aria-controls="automation-panel-overview" aria-selected="${state.automationTab === "overview"}" tabindex="${state.automationTab === "overview" ? "0" : "-1"}">Overview</button>
        <button id="automation-tab-flow" class="tab-button" type="button" role="tab" data-automation-tab="flow" aria-controls="automation-panel-flow" aria-selected="${state.automationTab === "flow"}" tabindex="${state.automationTab === "flow" ? "0" : "-1"}">Flow</button>
      </nav>
      <div class="detail-body" id="automation-panel-overview" role="tabpanel" aria-labelledby="automation-tab-overview" tabindex="0" ${state.automationTab === "overview" ? "" : "hidden"}>${state.automationTab === "overview" ? renderAutomationOverview(detail, runtime, execution, activeRevision, triggers, recentRuns, health) : ""}</div>
      <div class="detail-body" id="automation-panel-flow" role="tabpanel" aria-labelledby="automation-tab-flow" tabindex="0" ${state.automationTab === "flow" ? "" : "hidden"}>${state.automationTab === "flow" ? renderAutomationFlow(detail, activeRevision, triggers) : ""}</div>
    </article>`;
  }

  function renderAutomationOverview(detail, runtime, execution, activeRevision, triggers, recentRuns, health) {
    return `
        <dl class="facts">
          <div class="fact"><dt>Latest outcome</dt><dd class="${health.className}">${health.htmlLabel}</dd></div>
          <div class="fact"><dt>Next event</dt><dd>${nextEventSummary(triggers, detail.enabled)}</dd></div>
          <div class="fact"><dt>Current version</dt><dd>${activeRevision?.createdAt ? `Deployed ${relativeTimeElement(activeRevision.createdAt)}` : "Deployment time unavailable"}${activeRevision?.provenance ? ' · <span class="verification-inline">Verified</span>' : " · Not verified"}</dd></div>
          <div class="fact"><dt>Run safety</dt><dd>${escapeHTML(executionProtection(execution))}</dd></div>
        </dl>
        ${renderAutomationTechnicalDetails(detail, activeRevision, runtime, execution)}

        <section class="workspace-section" aria-labelledby="triggers-heading">
          <div class="section-heading"><div><h3 id="triggers-heading">Triggers</h3><p>${triggers.length} configured event ${triggers.length === 1 ? "source" : "sources"}</p></div></div>
          ${renderTriggers(triggers, detail.enabled)}
        </section>

        <section class="workspace-section" aria-labelledby="runs-heading">
          <div class="section-heading"><div><h3 id="runs-heading">Run history</h3><p>${escapeHTML(runHistorySummary(state.detailRuns))}</p></div><button class="section-link" type="button" data-view-link="runs">View all runs</button></div>
          ${state.detailRunsError ? `<div class="inline-notice is-error" role="status"><span>${escapeHTML(state.detailRunsError)}</span><button class="button button-quiet button-compact" type="button" data-retry-detail-runs>Try again</button></div>` : ""}
          ${renderRunsTable(recentRuns, true)}
        </section>

        <section class="workspace-section" aria-labelledby="revisions-heading">
          <div class="section-heading"><div><h3 id="revisions-heading">Version history</h3><p>${revisionCountLabel(Math.min(detail.revisions?.length || 0, 5))}</p></div></div>
          ${renderRevisions(detail.revisions || [])}
        </section>
    `;
  }

  function renderAutomationFlow(detail, activeRevision, triggers) {
    if (!state.flowRunID && state.detailRuns.length) state.flowRunID = state.detailRuns[0].id;
    const runSummary = state.detailRuns.find((run) => run.id === state.flowRunID) || state.detailRuns[0] || null;
    const runDetail = runSummary ? state.flowRunDetails[runSummary.id] : null;
    const run = runDetail || runSummary;
    const live = state.flowLayer === "live";
    const evidenceLoading = Boolean(live && run && state.flowRunLoading[run.id]);
    const evidenceError = live && run ? state.flowRunErrors[run.id] : "";
    const evidenceReady = Boolean(!live || runDetail);
    const observedTrigger = evidenceReady ? runDetail?.event?.trigger || null : null;
    const runRevision = live && run ? detail.revisions?.find((revision) => revision.id === run.revisionId) || null : activeRevision;
    const historical = Boolean(live && run && run.revisionId !== detail.activeRevisionId);
    const sourceModel = flowSources(triggers, observedTrigger, runDetail?.event?.metadata || {}, historical);
    const evidence = flowEvidence(detail, runRevision, sourceModel, live ? run : null, {historical, evidenceReady});
    const runOptions = state.detailRuns.map((item) => `<option value="${escapeHTML(item.id)}"${item.id === run?.id ? " selected" : ""}>${escapeHTML(shortID(item.id, 26))} · ${escapeHTML(capitalize(item.status))} · ${escapeHTML(formatDate(item.createdAt))}</option>`).join("");
    return `<section class="flow-view" aria-labelledby="flow-heading">
      <header class="flow-heading">
        <div><h3 id="flow-heading">Automation flow</h3><p>The code and manifest stay authoritative. This view projects their stable shape and overlays recorded evidence.</p></div>
        <div class="flow-controls">
          <div class="segmented-control" role="group" aria-label="Flow layer">
            <button type="button" data-flow-layer="definition" aria-pressed="${!live}" class="${!live ? "is-active" : ""}">Definition</button>
            <button type="button" data-flow-layer="live" aria-pressed="${live}" class="${live ? "is-active" : ""}" ${state.detailRuns.length ? "" : "disabled"}>Live run</button>
          </div>
          ${live ? `<label class="flow-run-picker"><span>Recorded run</span><select id="flow-run-select" data-flow-run ${state.detailRuns.length ? "" : "disabled"}>${runOptions}</select></label>` : ""}
          ${live && run ? `<button class="button button-quiet button-compact flow-diagnosis-action" type="button" data-flow-focus="diagnosis" data-run="${escapeHTML(run.id)}">Open run diagnosis</button>` : ""}
        </div>
      </header>
      ${live && evidenceLoading ? '<div class="inline-notice" role="status"><span>Loading the selected run’s event, logs, output, and continuation provenance…</span></div>' : ""}
      ${live && evidenceError ? `<div class="inline-notice is-error" role="status"><span>${escapeHTML(evidenceError)}</span><button class="button button-quiet button-compact" type="button" data-flow-focus="retry-run" data-retry-flow-run="${escapeHTML(run.id)}">Retry run evidence</button></div>` : ""}
      ${live && state.detailApprovalsError ? `<div class="inline-notice is-error" role="status"><span>${escapeHTML(state.detailApprovalsError)}</span><button class="button button-quiet button-compact" type="button" data-flow-focus="retry-approvals" data-retry-detail-approvals>Retry approval evidence</button></div>` : ""}
      ${historical ? `<div class="inline-notice flow-history-notice" role="status"><span>This run used revision <code>${escapeHTML(run.revisionId)}</code>. The topology still reflects the active manifest; unavailable historical manifest fields are labeled below.</span></div>` : ""}
      ${live && !run ? `<div class="empty-state flow-empty"><div class="empty-state-inner"><h2>No recorded run to overlay</h2><p>Trigger this automation to connect its definition to runtime evidence.</p></div></div>` : `
      <div class="flow-map${live && evidenceReady ? " has-recorded-path" : ""}" aria-label="${escapeHTML(live && run ? `${evidenceReady ? "Recorded path" : "Run evidence loading"} for run ${run.id}` : `Definition map for ${detail.id}`)}">
        <section class="flow-stage flow-sources" aria-labelledby="flow-sources-title">
          <div class="flow-stage-title"><h4 id="flow-sources-title">Event sources</h4><span>${triggers.length} ${triggers.length === 1 ? "declaration" : "declarations"} in active manifest</span></div>
          <div class="flow-node-stack">${sourceModel.length ? sourceModel.map((source) => `<button class="flow-node flow-source${source.observed ? " is-observed" : ""}" type="button" data-flow-evidence="${escapeHTML(source.key)}" aria-pressed="${state.flowEvidence === source.key}"><span class="flow-node-icon">${icon(icons[source.type] || "code")}</span><span><strong>${escapeHTML(source.id)}</strong><small>${escapeHTML(source.detail)}</small></span>${source.observed ? '<span class="observed-mark">Observed</span>' : ""}</button>`).join("") : `<div class="flow-node is-muted"><span><strong>No manifest triggers</strong><small>Manual runs remain available</small></span></div>`}</div>
        </section>
        <div class="flow-connector" aria-hidden="true"><span></span></div>
        <section class="flow-stage flow-runtime" aria-labelledby="flow-runtime-title">
          <div class="flow-stage-title"><h4 id="flow-runtime-title">Code revision</h4><span>${live ? "Pinned for run" : "Active definition"}</span></div>
          <button class="flow-node flow-core${live ? ` status-${statusClass(run.status)}` : ""}" type="button" data-flow-evidence="revision" aria-pressed="${state.flowEvidence === "revision"}">
            <span class="flow-core-top"><span class="flow-node-icon">${icon("code")}</span><span class="status-badge status-${live ? statusClass(run.status) : (detail.enabled ? "active" : "paused")}">${live ? escapeHTML(capitalize(run.status)) : (detail.enabled ? "Active" : "Paused")}</span></span>
            <strong>${escapeHTML(shortID(live ? run.revisionId : detail.activeRevisionId, 22))}</strong>
            <small>${historical ? "Historical runtime manifest unavailable" : `${escapeHTML(detail.manifest?.runtime?.language || "Executable")} · ${escapeHTML(detail.manifest?.execution?.concurrency || "allow")} concurrency`}</small>
            ${live ? `<span class="flow-run-id mono">${escapeHTML(run.id)}</span>` : `<span class="flow-run-id">Immutable deployed code</span>`}
          </button>
        </section>
        <div class="flow-connector" aria-hidden="true"><span></span></div>
        <section class="flow-stage flow-evidence" aria-labelledby="flow-evidence-title">
          <div class="flow-stage-title"><h4 id="flow-evidence-title">Recorded evidence</h4><span>${live ? "This run" : "On every run"}</span></div>
          <div class="flow-node-stack">
            <button class="flow-node${live ? " is-observed" : ""}" type="button" data-flow-evidence="record" aria-pressed="${state.flowEvidence === "record"}"><span class="flow-node-icon">${icon("runs")}</span><span><strong>Run record</strong><small>Status, attempts, and duration</small></span></button>
            <button class="flow-node${live && evidenceReady && run?.logs ? " is-observed" : ""}" type="button" data-flow-evidence="logs" aria-pressed="${state.flowEvidence === "logs"}"><span class="flow-node-icon">${icon("code")}</span><span><strong>Execution logs</strong><small>${live ? (!evidenceReady ? (evidenceError ? "Evidence unavailable" : "Loading run detail…") : (run?.logs ? "Recorded output available" : "No output recorded")) : "stdout and stderr"}</small></span></button>
            <button class="flow-node${live && evidenceReady && run?.result != null ? " is-observed" : ""}" type="button" data-flow-evidence="output" aria-pressed="${state.flowEvidence === "output"}"><span class="flow-node-icon">${icon("package")}</span><span><strong>Structured result</strong><small>${live ? (!evidenceReady ? (evidenceError ? "Evidence unavailable" : "Loading run detail…") : (run?.result != null ? "Recorded result available" : "No result recorded")) : "JSON result when emitted"}</small></span></button>
          </div>
        </section>
      </div>
      <aside class="flow-inspector" aria-live="polite"><div><span class="flow-inspector-label">Selected evidence</span><h4>${escapeHTML(evidence.title)}</h4><p>${escapeHTML(evidence.description)}</p></div><dl>${evidence.facts.map(([label, value]) => `<div><dt>${escapeHTML(label)}</dt><dd class="${label.includes("ID") || label === "Revision" ? "mono" : ""}">${escapeHTML(value)}</dd></div>`).join("")}</dl></aside>
      ${live && run && !evidenceReady ? renderUnavailableLedger(run, evidenceError ? "unavailable" : "loading") : renderFlowLedger(detail, runRevision, triggers, live ? runDetail : null)}
      ${live && run && evidenceReady && !state.detailApprovalsError ? renderActorJourney(runDetail, state.detailApprovals, "automation-flow-journey") : ""}
      `}
    </section>`;
  }

  function flowSources(triggers, observedTrigger, metadata, historical) {
    const systemSource = ["manual", "approval", "deferred"].includes(metadata.source) || ["manual", "approval", "deferred"].includes(observedTrigger?.type);
    const activeObserved = Boolean(observedTrigger && !historical && !systemSource && triggers.some((trigger) => trigger.id === observedTrigger.id && trigger.type === observedTrigger.type));
    const values = triggers.map((trigger) => ({
      key: `trigger:${trigger.id}`,
      id: trigger.id,
      type: trigger.type,
      trigger,
      observed: Boolean(activeObserved && observedTrigger.id === trigger.id),
      detail: `${trigger.type} · ${triggerConfiguration(trigger)}`,
    }));
    if (observedTrigger && !activeObserved) {
      const label = systemSource ? `${observedTrigger.type} · recorded system or operator source` : `${observedTrigger.type} · historical configuration unavailable`;
      values.push({key: "recorded-source", id: observedTrigger.id, type: observedTrigger.type, observed: true, recorded: true, systemSource, detail: label});
    }
    return values;
  }

  function flowEvidence(detail, revision, sources, run, options = {}) {
    const {historical = false, evidenceReady = false} = options;
    const key = state.flowEvidence;
    if (key === "recorded-source" || key.startsWith("trigger:")) {
      const source = sources.find((item) => item.key === key);
      if (source?.recorded) return {title: source.id, description: source.systemSource ? "Recorded by Werkt’s management or continuation machinery, not declared as a manifest trigger." : "Recorded on the selected historical run. Its original trigger configuration is not retained in this view.", facts: [["Type", source.type], ["Configuration", "Unavailable for this run"], ["Evidence", "Recorded event source"]]};
      if (source) return {title: source.id, description: source.observed ? "Declared by the active manifest and recorded as this run’s source." : "Declared in the active manifest. No claim is made that the selected historical run used this configuration.", facts: [["Type", source.type], ["Configuration", triggerConfiguration(source.trigger)], ["State", detail.enabled && source.trigger.enabled ? "Enabled" : "Paused"]]};
    }
    if (key === "record") return {title: "Run record", description: run ? "The durable execution record selected for this overlay." : "Werkt creates a durable run record for each accepted event.", facts: [["Run ID", run?.id || "Created at runtime"], ["Status", run ? capitalize(run.status) : "Recorded at runtime"], ["Attempt", run ? `${run.attempt}/${run.maxAttempts}` : "Bounded by the manifest"]]};
    if (key === "logs") return {title: "Execution logs", description: !evidenceReady && run ? "Run detail is still loading or unavailable; absence is not inferred." : (run?.logs ? "The selected run produced captured stdout or stderr." : "Logs stay attached to the exact run and revision that produced them."), facts: [["Availability", !evidenceReady && run ? "Loading or unavailable" : (run?.logs ? "Recorded" : "None recorded")], ["Run ID", run?.id || "Created at runtime"]]};
    if (key === "output") return {title: "Structured result", description: !evidenceReady && run ? "Run detail is still loading or unavailable; absence is not inferred." : (run?.result != null ? "The selected run returned a structured result." : "Code may return a JSON result; Werkt stores it on the exact run."), facts: [["Availability", !evidenceReady && run ? "Loading or unavailable" : (run?.result != null ? "Recorded" : "None recorded")], ["Run ID", run?.id || "Created at runtime"]]};
    return {title: "Immutable code revision", description: run ? "The run summary pins this revision identity; detail evidence augments it without changing the active topology." : "The deployed package remains the source of execution behavior; the map does not replace or rewrite it.", facts: [["Revision", run?.revisionId || detail.activeRevisionId], ["Content hash", revision?.contentHash || "Unavailable for this revision"], ["Runtime", historical ? "Historical manifest unavailable" : (detail.manifest?.runtime?.image || "Local process")]]};
  }

  function approvalForRun(run, approvals) {
    return approvals.find((approval) => approval.requestedByRunId === run.id || approval.actionRunId === run.id || approval.id === run.event?.metadata?.approvalId) || null;
  }

  function journeySteps(run, approvals) {
    const metadata = run.event?.metadata || {};
    const approval = approvalForRun(run, approvals);
    const steps = [];
    if (metadata.source === "deferred") steps.push({time: run.event?.receivedAt, actor: "Werkt", kind: "timer", title: "Continuation scheduled", detail: `${run.event?.id || run.eventId} · available ${exactTimestamp(run.event?.occurredAt)} · parent ${metadata.parentRunId || "unavailable"}`, state: "scheduled"});
    else if (metadata.source === "approval") steps.push({time: run.event?.receivedAt, actor: metadata.actor || "Operator", kind: "human", title: "Decision recorded; continuation queued", detail: `${run.event?.id || run.eventId} · ${metadata.approvalId || approval?.id || "approval unavailable"}`, state: "queued"});
    else if (metadata.source === "manual") steps.push({time: run.event?.receivedAt, actor: metadata.actor || "Operator or agent", kind: "actor", title: "Manual run requested", detail: `${run.event?.id || run.eventId} · ${run.event?.trigger?.id || "manual"}`, state: "recorded"});
    else steps.push({time: run.event?.receivedAt || run.createdAt, actor: "Werkt", kind: "system", title: "Event received", detail: run.event ? `${run.event.id} · ${run.event.trigger.type} · ${run.event.trigger.id}` : run.eventId || "Event identity available in run detail", state: "durable"});
    steps.push({time: run.startedAt || run.createdAt, actor: run.startedAt ? "Code" : "Werkt", kind: run.startedAt ? "code" : "system", title: run.startedAt ? "Revision started" : "Run created", detail: `${run.id} · ${run.revisionId} · attempt ${run.attempt}/${run.maxAttempts}`, state: run.startedAt ? "running" : "queued"});
    if (approval?.requestedByRunId === run.id) {
      steps.push({time: approval.createdAt, actor: "Werkt", kind: "system", title: "Approval requested for Operator", detail: `${approval.id} · ${approval.title}`, state: approval.status === "pending" ? "waiting" : approval.status});
      if (approval.resolvedAt) steps.push({time: approval.resolvedAt, actor: approval.resolvedBy || "Operator", kind: "human", title: `Decision ${approval.status}`, detail: approval.actionRunId ? `Continuation ${approval.actionRunId}` : approval.id, state: "recorded"});
    }
    if (run.finishedAt) steps.push({time: run.finishedAt, actor: "Werkt", kind: "system", title: `Run ${run.status}`, detail: `${run.id} · ${run.error || (run.result != null ? "structured result recorded" : "terminal state recorded")}`, state: run.status});
    return steps.sort((left, right) => new Date(left.time || "9999-12-31").getTime() - new Date(right.time || "9999-12-31").getTime());
  }

  function renderActorJourney(run, approvals, id) {
    const steps = journeySteps(run, approvals);
    const hasBoundary = Boolean(approvalForRun(run, approvals)) || steps.some((step) => ["human", "timer", "actor"].includes(step.kind));
    if (!hasBoundary) return "";
    return `<section class="actor-journey" aria-labelledby="${id}-title"><div class="section-heading"><div><h4 id="${id}-title">Continuation journey</h4><p>Actor lanes appear because this run crosses a human or timed boundary.</p></div></div><ol>${steps.map((step) => `<li class="journey-step is-${step.kind}"><span class="journey-actor">${escapeHTML(step.actor)}</span><span class="journey-line" aria-hidden="true"><i></i></span><span class="journey-event"><strong>${escapeHTML(step.title)}</strong><small>${escapeHTML(step.detail)}</small></span><span class="journey-state">${escapeHTML(step.state)}</span></li>`).join("")}</ol></section>`;
  }

  function renderFlowLedger(detail, activeRevision, triggers, run) {
    const definitionRows = [
      ...triggers.map((trigger) => ({time: null, actor: "Manifest", kind: "code", title: "Event source declaration", detail: `${trigger.type} · ${trigger.id} · ${triggerConfiguration(trigger)}`, state: detail.enabled && trigger.enabled ? "enabled" : "paused"})),
      {time: null, actor: "Manifest", kind: "code", title: "Immutable revision", detail: `${run?.revisionId || detail.activeRevisionId} · content ${activeRevision?.contentHash || "unavailable"}`, state: run?.revisionId === detail.activeRevisionId || !run ? "active context" : "historical context"},
      {time: null, actor: "Manifest", kind: "code", title: "Runtime definition", detail: run && run.revisionId !== detail.activeRevisionId ? "historical runtime manifest unavailable" : `${detail.manifest?.runtime?.image || "local process"} · ${detail.manifest?.execution?.concurrency || "allow"} concurrency`, state: "definition"},
    ];
    const rows = run ? [...definitionRows, ...recordedLedgerRows(run, state.detailApprovals)] : definitionRows;
    const title = run ? "Exact flow ledger" : "Definition facts";
    return `<details class="flow-ledger" open><summary data-flow-focus="ledger-summary">${title} <span>${rows.length} ${rows.length === 1 ? "record" : "records"}</span></summary><div class="flow-ledger-table" role="table" aria-label="${title}"><div class="flow-ledger-head" role="row"><span role="columnheader">Time</span><span role="columnheader">Actor</span><span role="columnheader">Recorded change</span><span role="columnheader">State</span></div>${rows.map((row) => `<div class="flow-ledger-row" role="row">${exactTimeCell(row.time)}<span role="cell" data-label="Actor" class="ledger-actor is-${escapeHTML(row.kind)}">${escapeHTML(row.actor)}</span><span role="cell" data-label="Recorded change"><strong>${escapeHTML(row.title)}</strong><code>${escapeHTML(row.detail)}</code></span><span role="cell" data-label="State">${escapeHTML(row.state)}</span></div>`).join("")}</div></details>`;
  }

  function recordedLedgerRows(run, approvals) {
    const event = run.event;
    const metadata = event?.metadata || {};
    const approval = approvalForRun(run, approvals);
    const sourceActor = metadata.source === "manual" || metadata.source === "approval" ? (metadata.actor || "Operator or agent") : (event?.trigger?.type ? capitalize(event.trigger.type) : "Event source");
    const occurredTitle = metadata.source === "deferred" ? "Scheduled availability" : (metadata.source === "approval" ? "Approval decision occurred" : "Event occurred");
    const receivedTitle = metadata.source === "deferred" ? "Continuation request recorded" : (metadata.source === "approval" ? "Approval decision received" : "Event received");
    const rows = [];
    if (event?.occurredAt) rows.push({time: event.occurredAt, actor: sourceActor, kind: metadata.source === "deferred" ? "timer" : (metadata.actor ? "actor" : "system"), title: occurredTitle, detail: `${event.id} · ${event.trigger.type} · ${event.trigger.id}`, state: metadata.source === "deferred" ? "scheduled" : "occurred"});
    if (event?.receivedAt) rows.push({time: event.receivedAt, actor: "Werkt", kind: "system", title: receivedTitle, detail: `${event.id} · source ${metadata.source || event.trigger.type}`, state: "durable"});
    rows.push({time: run.createdAt, actor: "Werkt", kind: "system", title: "Run created", detail: `${run.id} · ${run.revisionId} · attempt ${run.attempt}/${run.maxAttempts}`, state: "queued"});
    if (run.startedAt) rows.push({time: run.startedAt, actor: "Code", kind: "code", title: "Revision started", detail: `${run.id} · ${run.revisionId}`, state: "running"});
    if (approval?.requestedByRunId === run.id) {
      rows.push({time: approval.createdAt, actor: "Werkt", kind: "system", title: "Approval requested for Operator", detail: `${approval.id} · ${approval.title}`, state: approval.status === "pending" ? "waiting" : approval.status});
      if (approval.resolvedAt) rows.push({time: approval.resolvedAt, actor: approval.resolvedBy || "Operator", kind: "human", title: `Decision ${approval.status}`, detail: `${approval.id}${approval.actionRunId ? ` · continuation ${approval.actionRunId}` : ""}`, state: "recorded"});
    }
    if (run.finishedAt) rows.push({time: run.finishedAt, actor: "Werkt", kind: "system", title: `Run ${run.status}`, detail: `${run.id} · logs ${run.logs ? "recorded" : "absent"} · result ${run.result != null ? "recorded" : "absent"}${run.error ? ` · ${run.error}` : ""}`, state: run.status});
    return rows.map((row, index) => ({...row, index})).sort((left, right) => new Date(left.time).getTime() - new Date(right.time).getTime() || left.index - right.index);
  }

  function exactTimeCell(value) {
    if (!value) return '<span role="cell" data-label="Time" class="tabular">Definition</span>';
    const date = new Date(value);
    const exact = Number.isNaN(date.valueOf()) ? String(value) : date.toISOString();
    return `<time role="cell" data-label="Time" class="tabular" datetime="${escapeHTML(exact)}">${escapeHTML(exact)}</time>`;
  }

  function exactTimestamp(value) {
    if (!value) return "unavailable";
    const date = new Date(value);
    return Number.isNaN(date.valueOf()) ? String(value) : date.toISOString();
  }

  function renderUnavailableLedger(run, status) {
    const label = status === "loading" ? "Loading exact run evidence" : "Exact run evidence unavailable";
    return `<details class="flow-ledger" open><summary data-flow-focus="ledger-summary">Exact flow ledger <span>${escapeHTML(status)}</span></summary><div class="flow-ledger-table" role="table" aria-label="Exact flow ledger"><div class="flow-ledger-head" role="row"><span role="columnheader">Time</span><span role="columnheader">Actor</span><span role="columnheader">Recorded change</span><span role="columnheader">State</span></div><div class="flow-ledger-row" role="row">${exactTimeCell(run.createdAt)}<span role="cell" data-label="Actor" class="ledger-actor is-system">Werkt</span><span role="cell" data-label="Recorded change"><strong>${escapeHTML(label)}</strong><code>${escapeHTML(run.id)} · revision ${escapeHTML(run.revisionId)} · status ${escapeHTML(run.status)}</code></span><span role="cell" data-label="State">${escapeHTML(status)}</span></div></div></details>`;
  }

  async function selectAutomationTab(tab, moveFocus = true) {
    if (!["overview", "flow"].includes(tab)) return;
    state.automationTab = tab;
    if (tab === "flow" && state.flowLayer === "live" && !state.flowRunID) state.flowRunID = state.detailRuns[0]?.id || "";
    renderAutomationDetail();
    if (tab === "flow" && state.flowLayer === "live") await loadFlowRun(state.flowRunID);
    if (moveFocus) workspaceContent.querySelector(`[data-automation-tab="${tab}"]`)?.focus();
  }

  function currentFlowFocusSelector(preferred = "") {
    if (preferred) return preferred;
    const active = document.activeElement;
    if (active?.dataset?.flowFocus) return `[data-flow-focus="${CSS.escape(active.dataset.flowFocus)}"]`;
    if (active?.matches?.("[data-flow-run]")) return "[data-flow-run]";
    if (active?.dataset?.flowLayer) return `[data-flow-layer="${CSS.escape(active.dataset.flowLayer)}"]`;
    if (active?.dataset?.flowEvidence) return `[data-flow-evidence="${CSS.escape(active.dataset.flowEvidence)}"]`;
    if (active?.dataset?.automationTab) return `[data-automation-tab="${CSS.escape(active.dataset.automationTab)}"]`;
    return "";
  }

  function renderAutomationDetailPreservingFocus(preferred = "") {
    const selector = currentFlowFocusSelector(preferred);
    renderAutomationDetail();
    if (selector) (workspaceContent.querySelector(selector) || workspaceContent.querySelector('[data-flow-layer="live"]'))?.focus();
  }

  async function retryFlowApprovals() {
    if (!state.selectedAutomation) return;
    const selector = '[data-flow-focus="retry-approvals"]';
    state.detailApprovalsError = "";
    renderAutomationDetailPreservingFocus(selector);
    try {
      const approvals = await api(`/api/v1/approvals?automation=${encodeURIComponent(state.selectedAutomation)}&limit=${HISTORY_LIMIT}`, {page: true});
      state.detailApprovals = approvals.items;
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) state.detailApprovalsError = detailApprovalsMessage(error);
    } finally {
      if (state.automationTab === "flow") renderAutomationDetailPreservingFocus(selector);
    }
  }

  async function loadFlowRun(runID, {force = false, focusSelector = ""} = {}) {
    if (!runID) return;
    if (state.flowRunDetails[runID] && !force) {
      renderAutomationDetailPreservingFocus(focusSelector);
      return;
    }
    state.flowRunLoading[runID] = true;
    delete state.flowRunErrors[runID];
    if (state.automationTab === "flow") renderAutomationDetailPreservingFocus(focusSelector);
    try {
      const run = await api(`/api/v1/runs/${encodeURIComponent(runID)}`);
      if (state.flowRunID !== runID || state.automationTab !== "flow") return;
      state.flowRunDetails[runID] = run;
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) state.flowRunErrors[runID] = `Run evidence could not be loaded. ${recoveryGuidance(error)}`;
    } finally {
      delete state.flowRunLoading[runID];
      if (state.flowRunID === runID && state.automationTab === "flow") renderAutomationDetailPreservingFocus(focusSelector);
    }
  }

  function renderTriggers(triggers, automationEnabled) {
    if (!triggers.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No triggers configured</h2><p>Declare an event source in <span class="mono">automation.yaml</span> and deploy a new revision.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="wide">Trigger</th><th>Configuration</th><th class="hide-tablet">Next event</th><th class="status-column">State</th></tr></thead><tbody>${triggers.map((trigger) => {
      const effective = Boolean(automationEnabled && trigger.enabled);
      return `<tr><td data-label="Trigger"><span class="trigger-type">${icon(icons[trigger.type] || "code")}<span>${escapeHTML(trigger.id)}</span></span></td><td data-label="Config">${escapeHTML(triggerConfiguration(trigger))}</td><td data-label="Next" class="hide-tablet tabular">${trigger.nextFireAt ? relativeTimeElement(trigger.nextFireAt) : "On event"}</td><td data-label="State"><span class="status-badge status-${effective ? "active" : "paused"}">${effective ? "Enabled" : "Paused"}</span></td></tr>`;
    }).join("")}</tbody></table>`;
  }

  function triggerConfiguration(trigger) {
    const config = trigger.config || {};
    if (trigger.type === "schedule") return `${config.cron || "No cron"} · ${config.timezone || "UTC"}`;
    if (trigger.type === "webhook" && config.provider === "zoom") return "Zoom · verified by Werkt";
    if (trigger.type === "webhook" && config.provider === "github") return "GitHub · verified by Werkt";
    if (trigger.type === "webhook") return `HMAC · ${config.signatureHeader || "X-Werkt-Signature"}`;
    if (trigger.type === "email") return "Bearer-authenticated RFC 5322";
    if (trigger.type === "ntfy") return `${config.server || "ntfy"}/${config.topic || "topic"}`;
    return "Event source";
  }

  function renderRunsTable(runs, compact = false) {
    if (!runs.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No runs yet</h2><p>${compact ? "Trigger this automation" : "Trigger an automation"} or queue a manual diagnostic run to see execution history.</p></div></div>`;
    return `<table class="data-table runs-table"><thead><tr><th class="status-column">Status</th>${compact ? "" : "<th>Automation</th>"}<th>Started</th><th class="hide-tablet">Attempt</th><th class="hide-tablet">Duration</th><th class="run-actions-column"><span class="sr-only">Run actions</span></th></tr></thead><tbody>${runs.map((run) => `<tr><td data-label="Status"><span class="status-badge status-${statusClass(run.status)}">${escapeHTML(capitalize(run.status))}</span></td>${compact ? "" : `<td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(run.automationId)}">${escapeHTML(run.automationId)}</button></td>`}<td data-label="Started" class="tabular">${relativeTimeElement(run.startedAt || run.createdAt)}</td><td data-label="Attempt" class="hide-tablet">${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</td><td data-label="Duration" class="hide-tablet tabular">${escapeHTML(runDuration(run))}</td><td data-label="Actions" class="run-actions-column"><div class="row-actions"><button class="button button-quiet button-compact" type="button" data-run="${escapeHTML(run.id)}" aria-label="Inspect ${escapeHTML(run.automationId)} run from ${escapeHTML(formatDate(run.startedAt || run.createdAt))}">Inspect</button><button class="button button-quiet button-compact" type="button" data-run-logs="${escapeHTML(run.id)}" aria-label="View logs for ${escapeHTML(run.automationId)} run from ${escapeHTML(formatDate(run.startedAt || run.createdAt))}">Logs</button></div></td></tr>`).join("")}</tbody></table>`;
  }

  function renderRevisions(revisions) {
    if (!revisions.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No revisions available</h2><p>Deploy the automation package to create its first immutable revision.</p></div></div>`;
    let previous = 0;
    return `<table class="data-table revision-table"><thead><tr><th class="version-column">Version</th><th>Deployed</th><th>By</th><th>Verification</th><th class="revision-action-column"><span class="sr-only">Version action</span></th></tr></thead><tbody>${revisions.slice(0, 5).map((revision) => {
      if (!revision.active) previous += 1;
      const version = revision.active ? "Current" : previous === 1 ? "Previous" : `${previous} versions back`;
      const deployment = state.deployments.find((item) => item.revisionId === revision.id);
      const verification = revision.provenance ? "Verified" : "Not verified";
      return `<tr${revision.active ? ' class="revision-current"' : ""}><td data-label="Version"><strong>${escapeHTML(version)}</strong><details class="technical-disclosure revision-technical"><summary>Technical details</summary><dl class="technical-grid">${technicalFact("Revision", revision.id)}${technicalFact("Content SHA-256", revision.contentHash)}${technicalFact("Artifact SHA-256", revision.provenance?.artifactDigest)}</dl></details></td><td data-label="Deployed" class="tabular">${relativeTimeElement(revision.createdAt)}</td><td data-label="By" title="${escapeHTML(deployment?.actor || "")}">${deployment ? escapeHTML(actorLabel(deployment.actor)) : '<span class="muted-value">Not recorded</span>'}</td><td data-label="Verification"><span class="verification-state ${revision.provenance ? "is-verified" : ""}">${escapeHTML(verification)}</span></td><td data-label="Action" class="revision-action-column">${revision.active ? '<span class="current-version-label">In use</span>' : `<button class="button button-quiet button-compact" type="button" data-rollback-revision="${escapeHTML(revision.id)}" ${state.mutating ? "disabled" : ""}>${icon("rollback")}Restore</button>`}</td></tr>`;
    }).join("")}</tbody></table>`;
  }

  function revisionCountLabel(count) {
    return `${count} recent deployed ${count === 1 ? "version" : "versions"}`;
  }

  function revisionDisplayName(revisionID) {
    let previous = 0;
    for (const revision of state.detail?.revisions || []) {
      if (revision.active) {
        if (revision.id === revisionID) return "current version";
        continue;
      }
      previous += 1;
      if (revision.id === revisionID) return previous === 1 ? "previous version" : `version from ${formatDate(revision.createdAt)}`;
    }
    return "selected version";
  }

  function renderGlobalApprovals() {
    const approvals = state.approvalStatusFilter === "all"
      ? [...state.approvals].sort((left, right) => Number(right.status === "pending") - Number(left.status === "pending"))
      : state.approvals.filter((approval) => approval.status === state.approvalStatusFilter);
    const pending = state.approvals.filter((approval) => approval.status === "pending").length;
    workspaceContent.innerHTML = `<section class="global-view approvals-view"><header class="global-view-header"><div><h1>Approvals</h1><p>${pending ? `${pending} ${pending === 1 ? "decision needs" : "decisions need"} you.` : "No decisions are waiting."} Werkt safely resumes the same deployed version after your response.</p></div><div class="global-toolbar"><label class="sr-only" for="approval-status-filter">Filter approvals by status</label><select class="select-control" id="approval-status-filter"><option value="all">All statuses</option>${["pending", "approved", "rejected", "expired"].map((status) => `<option value="${status}"${state.approvalStatusFilter === status ? " selected" : ""}>${capitalize(status)}</option>`).join("")}</select></div></header>${feedNotice("approvals")}${state.feedLoading.approvals && !approvals.length ? "" : renderApprovalsTable(approvals)}${feedFooter("approvals")}</section>`;
  }

  function renderApprovalsTable(approvals) {
    if (!approvals.length) return `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("approval")}</span><h2>No approvals found</h2><p>${state.approvalStatusFilter === "pending" ? "New requests will appear here with their expiry, fields, and exact execution target." : "Choose another status to inspect past operator decisions."}</p></div></div>`;
    return `<table class="data-table approvals-table"><thead><tr><th class="status-column">Status</th><th class="approval-title-column">Decision</th><th>Automation</th><th>Requested</th><th>Expires</th><th class="approval-action-column"><span class="sr-only">Action</span></th></tr></thead><tbody>${approvals.map((approval) => `<tr${approval.status === "pending" ? ' class="approval-pending"' : ""}><td data-label="Status"><span class="status-badge status-${escapeHTML(approval.status)}">${escapeHTML(capitalize(approval.status))}</span></td><td data-label="Decision"><strong>${escapeHTML(approval.title)}</strong></td><td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(approval.automationId)}">${escapeHTML(approval.automationId)}</button></td><td data-label="Requested" class="tabular">${relativeTimeElement(approval.createdAt)}</td><td data-label="Expires" class="tabular" title="${escapeHTML(formatDate(approval.expiresAt))}">${relativeTimeElement(approval.expiresAt)}</td><td data-label="Action" class="approval-action-column">${approval.status === "pending" ? `<button class="button button-primary button-compact" type="button" data-approval="${escapeHTML(approval.id)}">Review</button>` : approval.actionRunId ? `<button class="button button-quiet button-compact" type="button" data-run="${escapeHTML(approval.actionRunId)}">View run</button>` : '<span class="muted-value">Closed</span>'}</td></tr>`).join("")}</tbody></table>`;
  }

  function renderGlobalRuns() {
    const runs = state.runStatusFilter === "all" ? state.runs : state.runs.filter((run) => run.status === state.runStatusFilter);
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Runs</h1><p>Recent runs across every automation. ${escapeHTML(historyScope(state.runs.length))}. Open a run to inspect attempts, logs, and structured output.</p></div><div class="global-toolbar"><label class="sr-only" for="run-status-filter">Filter runs by status</label><select class="select-control" id="run-status-filter"><option value="all">All statuses</option>${["queued", "running", "succeeded", "failed"].map((status) => `<option value="${status}"${state.runStatusFilter === status ? " selected" : ""}>${capitalize(status)}</option>`).join("")}</select></div></header>${feedNotice("runs")}${state.feedLoading.runs && !runs.length ? "" : renderRunsTable(runs)}${feedFooter("runs")}</section>`;
  }

  function renderGlobalDeployments() {
    const deployments = state.deploymentStatusFilter === "all"
      ? state.deployments
      : state.deployments.filter((deployment) => {
        if (state.deploymentStatusFilter === "in-progress") return !["succeeded", "failed", "cancelled"].includes(deployment.status);
        if (state.deploymentStatusFilter === "failed") return ["failed", "cancelled"].includes(deployment.status);
        return deployment.status === state.deploymentStatusFilter;
      });
    const activeCount = state.deployments.filter((deployment) => !deployment.finishedAt).length;
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Deployments</h1><p>Recent package promotions across every automation. ${escapeHTML(historyScope(state.deployments.length))}.${activeCount ? ` ${activeCount} ${activeCount === 1 ? "deployment is" : "deployments are"} still in progress.` : ""}</p></div><div class="global-toolbar"><label class="sr-only" for="deployment-status-filter">Filter deployments by status</label><select class="select-control" id="deployment-status-filter"><option value="all">All statuses</option>${["in-progress", "succeeded", "failed"].map((status) => `<option value="${status}"${state.deploymentStatusFilter === status ? " selected" : ""}>${status === "in-progress" ? "In progress" : status === "failed" ? "Failed or cancelled" : "Succeeded"}</option>`).join("")}</select></div></header>${feedNotice("deployments")}${state.feedLoading.deployments && !deployments.length ? "" : renderDeploymentsTable(deployments)}${feedFooter("deployments")}</section>`;
  }

  function renderDeploymentsTable(deployments) {
    if (!deployments.length) return `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("package")}</span><h2>No deployments found</h2><p>Upload an automation package with the CLI or adjust the status filter.</p><code class="empty-command">werkt deploy ./path/to/automation</code></div></div>`;
    return `<table class="data-table"><thead><tr><th class="status-column">Status</th><th>Automation</th><th>Received</th><th class="hide-tablet">By</th><th class="hide-tablet">Duration</th><th class="action-column"><span class="sr-only">Open</span></th></tr></thead><tbody>${deployments.map((deployment) => `<tr><td data-label="Status"><span class="status-badge status-${statusClass(deployment.status)}">${escapeHTML(capitalize(deployment.status))}</span></td><td data-label="Automation">${deployment.automationId ? `<button class="table-button" type="button" data-automation="${escapeHTML(deployment.automationId)}">${escapeHTML(deployment.automationId)}</button>` : '<span class="muted-value">Awaiting manifest</span>'}</td><td data-label="Received" class="tabular">${relativeTimeElement(deployment.createdAt)}</td><td data-label="By" class="hide-tablet" title="${escapeHTML(deployment.actor)}">${escapeHTML(actorLabel(deployment.actor))}</td><td data-label="Duration" class="hide-tablet tabular">${escapeHTML(deploymentDuration(deployment))}</td><td class="action-column"><button class="icon-button" type="button" data-deployment="${escapeHTML(deployment.id)}" aria-label="Inspect ${escapeHTML(deployment.automationId || "deployment")}">${icon("chevron")}</button></td></tr>`).join("")}</tbody></table>`;
  }

  function renderGlobalAudit() {
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Audit</h1><p>Recent lifecycle changes attributed through the management API. ${escapeHTML(historyScope(state.audit.length))}. Open an event for its exact timestamp and recorded context.</p></div></header>${feedNotice("audit")}${state.feedLoading.audit && !state.audit.length ? "" : renderAuditTable(state.audit)}${feedFooter("audit")}</section>`;
  }

  function renderAuditTable(events) {
    if (!events.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No audit activity yet</h2><p>Deployments and management mutations will appear here with actor attribution.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="wide">Action</th><th>Automation</th><th>By</th><th>When</th><th class="action-column"><span class="sr-only">Open</span></th></tr></thead><tbody>${events.map((event) => `<tr><td data-label="Action"><button class="table-button" type="button" data-audit="${escapeHTML(event.id)}">${escapeHTML(humanizeAction(event.action))}</button></td><td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(event.automationId)}">${escapeHTML(event.automationId)}</button></td><td data-label="By" title="${escapeHTML(event.actor)}">${escapeHTML(actorLabel(event.actor))}</td><td data-label="When" class="tabular">${relativeTimeElement(event.createdAt)}</td><td class="action-column"><button class="icon-button" type="button" data-audit="${escapeHTML(event.id)}" aria-label="Inspect ${escapeHTML(humanizeAction(event.action))}">${icon("chevron")}</button></td></tr>`).join("")}</tbody></table>`;
  }

  function feedFooter(kind) {
    if (!state.feedNextCursor[kind]) return "";
    return `<div class="feed-footer"><button class="button button-quiet" type="button" data-load-more="${kind}" ${state.feedLoading[kind] ? "disabled" : ""}>${state.feedLoading[kind] ? "Loading…" : "Load older records"}</button></div>`;
  }

  async function openRun(runID, initialTab = "summary", pushRoute = true) {
    const request = ++diagnosisRequest;
    diagnosisController?.abort();
    diagnosisController = new AbortController();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    runPollRequest += 1;
    runPollController?.abort();
    state.selectedDeployment = null;
    state.selectedRun = null;
    state.selectedAudit = null;
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    renderDiagnosisLoading("Loading run diagnosis…");
    syncDiagnosisModality();
    try {
      const run = await api(`/api/v1/runs/${encodeURIComponent(runID)}`, {signal: diagnosisController.signal});
      if (request !== diagnosisRequest) return;
      state.selectedRun = run;
      state.diagnosisTab = initialTab;
      if (pushRoute) writeDiagnosisRoute("run", run.id, "push");
      renderDiagnosis(true);
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) {
        diagnosisContent.innerHTML = `<div class="error-state"><div class="error-state-inner"><h2>Run could not be loaded</h2><p>${escapeHTML(recoveryGuidance(error))}</p><button class="button button-quiet" type="button" data-close-diagnosis>Close diagnosis</button></div></div>`;
      }
    }
  }

  async function openDeployment(deploymentID, pushRoute = true) {
    const request = ++diagnosisRequest;
    diagnosisController?.abort();
    diagnosisController = new AbortController();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    runPollRequest += 1;
    runPollController?.abort();
    state.selectedDeployment = null;
    state.selectedRun = null;
    state.selectedAudit = null;
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    renderDiagnosisLoading("Loading deployment details…");
    syncDiagnosisModality();
    try {
      const deployment = await api(`/api/v1/deployments/${encodeURIComponent(deploymentID)}`, {signal: diagnosisController.signal});
      if (request !== diagnosisRequest) return;
      state.selectedDeployment = deployment;
      if (pushRoute) writeDiagnosisRoute("deployment", deployment.id, "push");
      renderDeploymentDiagnosis(true);
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) {
        diagnosisContent.innerHTML = `<div class="error-state"><div class="error-state-inner"><h2>Deployment could not be loaded</h2><p>${escapeHTML(recoveryGuidance(error))}</p><button class="button button-quiet" type="button" data-close-diagnosis>Close details</button></div></div>`;
      }
    }
  }

  function openAudit(auditID, pushRoute = true) {
    const audit = state.audit.find((event) => event.id === auditID);
    if (!audit) {
      showToast("That audit event is no longer in the loaded history. Refresh Audit and try again.", true);
      return;
    }
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    runPollRequest += 1;
    runPollController?.abort();
    state.selectedRun = null;
    state.selectedDeployment = null;
    state.selectedAudit = audit;
    if (pushRoute) writeDiagnosisRoute("audit", audit.id, "push");
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Audit event</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close audit event">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-active">Recorded</span><strong>${escapeHTML(humanizeAction(audit.action))}</strong></div><p class="diagnosis-meta"><span>${escapeHTML(audit.automationId || "Workspace")}</span><span>${escapeHTML(formatDate(audit.createdAt))}</span></p></div><div class="diagnosis-body"><dl class="diagnosis-facts"><dt>Action</dt><dd>${escapeHTML(humanizeAction(audit.action))}</dd><dt>Automation</dt><dd>${escapeHTML(audit.automationId || "Workspace")}</dd><dt>By</dt><dd title="${escapeHTML(audit.actor)}">${escapeHTML(actorLabel(audit.actor))}</dd><dt>Recorded</dt><dd class="tabular">${escapeHTML(formatDate(audit.createdAt))}</dd></dl><details class="technical-disclosure"><summary>Technical details</summary><dl class="technical-grid">${technicalFact("Audit event", audit.id)}${technicalFact("Action key", audit.action)}${technicalFact("Actor identity", audit.actor)}</dl></details><h3>Recorded context</h3><pre class="code-block">${escapeHTML(prettyJSON(audit.details) || "No additional context was recorded.")}</pre></div>`;
    syncDiagnosisModality();
    diagnosisContent.querySelector("[data-close-diagnosis]")?.focus();
  }

  function closeDiagnosis({restoreFocus = true, syncRoute = true} = {}) {
    diagnosisRequest += 1;
    diagnosisController?.abort();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    runPollRequest += 1;
    runPollController?.abort();
    diagnosisPane.hidden = true;
    shell.classList.remove("has-diagnosis");
    syncDiagnosisModality();
    state.selectedRun = null;
    state.selectedDeployment = null;
    state.selectedAudit = null;
    if (syncRoute) writeDiagnosisRoute();
    const returnFocus = state.diagnosisReturnFocus;
    state.diagnosisReturnFocus = null;
    if (!restoreFocus) return;
    if (returnFocus?.isConnected) returnFocus.focus();
    else document.querySelector("#workspace-main")?.focus();
  }

  function renderDiagnosisLoading(label) {
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Details</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close details">${icon("close")}</button></div></div><div class="workspace-loading" role="status"><span class="sr-only">${escapeHTML(label)}</span><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-block"></div></div>`;
    diagnosisContent.querySelector("[data-close-diagnosis]")?.focus();
  }

  function syncDiagnosisModality() {
    const modal = !diagnosisPane.hidden && diagnosisModalQuery.matches;
    if (modal) {
      diagnosisPane.setAttribute("role", "dialog");
      diagnosisPane.setAttribute("aria-modal", "true");
    } else {
      diagnosisPane.removeAttribute("role");
      diagnosisPane.removeAttribute("aria-modal");
    }
    document.querySelectorAll(".topbar, .primary-nav, .inventory-pane, .workspace-main").forEach((element) => {
      element.inert = modal;
    });
  }

  function renderDiagnosis(focusPanel = false) {
    const run = state.selectedRun;
    if (!run) return;
    const tabs = ["summary", "journey", "logs", "output"];
    const activeTab = state.diagnosisTab;
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Run details</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close run details">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-${statusClass(run.status)}" data-run-status>${escapeHTML(capitalize(run.status))}</span><strong>${escapeHTML(run.automationId)}</strong></div><p class="diagnosis-meta"><span>${relativeTimeElement(run.startedAt || run.createdAt)}</span><span data-run-duration>${escapeHTML(runDuration(run))}</span><span>attempt <span data-run-attempt>${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</span></span></p><div class="diagnosis-tabs" role="tablist" aria-label="Run detail">${tabs.map((tab) => `<button id="diagnosis-tab-${tab}" class="tab-button" type="button" role="tab" data-diagnosis-tab="${tab}" aria-controls="diagnosis-panel-${tab}" aria-selected="${activeTab === tab}" tabindex="${activeTab === tab ? "0" : "-1"}">${capitalize(tab)}</button>`).join("")}</div></div><div class="diagnosis-body">${renderRunFailure(run)}${tabs.map((tab) => `<div id="diagnosis-panel-${tab}" role="tabpanel" aria-labelledby="diagnosis-tab-${tab}" tabindex="0" ${activeTab === tab ? "" : "hidden"}>${activeTab === tab ? diagnosisTabContent(run) : ""}</div>`).join("")}</div>`;
    if (focusPanel) diagnosisContent.querySelector("[data-close-diagnosis]").focus();
  }

  function renderDeploymentDiagnosis(focusPanel = false) {
    const deployment = state.selectedDeployment;
    if (!deployment) return;
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Deployment details</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close deployment details">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-${statusClass(deployment.status)}" data-deployment-status>${escapeHTML(capitalize(deployment.status))}</span><strong>${escapeHTML(deployment.automationId || "Package validation")}</strong></div><p class="diagnosis-meta"><span data-deployment-duration>${escapeHTML(deploymentDuration(deployment))}</span><span title="${escapeHTML(deployment.actor)}">${escapeHTML(actorLabel(deployment.actor))}</span></p><div data-deployment-actions>${deploymentActions(deployment)}</div></div><div class="diagnosis-body">${deploymentDiagnosisBody(deployment)}</div>`;
    if (focusPanel) diagnosisContent.querySelector("[data-close-diagnosis]").focus();
  }

  function updateRunDiagnosis(run) {
    const status = diagnosisContent.querySelector("[data-run-status]");
    if (!status) {
      renderDiagnosis();
      return;
    }
    status.className = `status-badge status-${statusClass(run.status)}`;
    status.textContent = capitalize(run.status);
    diagnosisContent.querySelector("[data-run-duration]").textContent = runDuration(run);
    diagnosisContent.querySelector("[data-run-attempt]").textContent = `${run.attempt}/${run.maxAttempts}`;
    const body = diagnosisContent.querySelector(".diagnosis-body");
    if (!body.contains(document.activeElement)) body.innerHTML = `${renderRunFailure(run)}${["summary", "journey", "logs", "output"].map((tab) => `<div id="diagnosis-panel-${tab}" role="tabpanel" aria-labelledby="diagnosis-tab-${tab}" tabindex="0" ${state.diagnosisTab === tab ? "" : "hidden"}>${state.diagnosisTab === tab ? diagnosisTabContent(run) : ""}</div>`).join("")}`;
  }

  function deploymentActions(deployment) {
    const terminal = ["succeeded", "failed", "cancelled"].includes(deployment.status);
    if (!terminal && !deployment.cancelRequestedAt) return `<div class="deployment-actions"><button class="button button-danger" type="button" data-cancel-deployment="${escapeHTML(deployment.id)}" ${state.mutating ? "disabled" : ""}>Cancel deployment</button><button class="section-link" type="button" data-open-help="help-lifecycle">Safety guide</button></div>`;
    if (["failed", "cancelled"].includes(deployment.status)) return `<div class="deployment-actions"><button class="button button-primary" type="button" data-retry-deployment="${escapeHTML(deployment.id)}" ${state.mutating ? "disabled" : ""}>${icon("refresh")}Retry package</button><button class="section-link" type="button" data-open-help="help-recovery">Recovery guide</button></div>`;
    return "";
  }

  function deploymentDiagnosisBody(deployment) {
    const failure = deployment.status === "failed" && deployment.error
      ? `<section class="deployment-error" aria-labelledby="deployment-error-title"><h3 id="deployment-error-title">Failure</h3><p>${escapeHTML(deployment.error)}</p></section>`
      : "";
    const cancellation = deployment.cancelRequestedAt && deployment.status !== "cancelled"
      ? `<section class="deployment-notice" aria-labelledby="deployment-cancellation-title"><h3 id="deployment-cancellation-title">Cancellation requested</h3><p>Werkt is stopping the active promotion command. Requested ${relativeTimeElement(deployment.cancelRequestedAt)}.</p></section>`
      : "";
    const provenance = deployment.provenance;
    return `${failure}${cancellation}${renderDeploymentSteps(deployment.steps || [])}<dl class="diagnosis-facts"><dt>Automation</dt><dd>${escapeHTML(deployment.automationId || "Awaiting manifest")}</dd><dt>Started by</dt><dd title="${escapeHTML(deployment.actor)}">${escapeHTML(actorLabel(deployment.actor))}</dd><dt>Received</dt><dd class="tabular">${escapeHTML(formatDate(deployment.createdAt))}</dd><dt>Started</dt><dd class="tabular">${escapeHTML(formatDate(deployment.startedAt))}</dd><dt>Finished</dt><dd class="tabular">${escapeHTML(formatDate(deployment.finishedAt))}</dd><dt>Updated</dt><dd class="tabular">${escapeHTML(formatDate(deployment.updatedAt))}</dd></dl><details class="technical-disclosure"><summary>Technical details</summary><p>Exact package, version, and provenance identifiers for verification and support.</p><dl class="technical-grid">${technicalFact("Deployment", deployment.id)}${technicalFact("Revision", deployment.revisionId)}${deployment.retryOf ? technicalFact("Retry of", deployment.retryOf) : ""}${technicalFact("Package SHA-256", deployment.packageDigest)}${technicalFact("Content SHA-256", deployment.contentHash)}${technicalFact("Artifact SHA-256", provenance?.artifactDigest)}${technicalFact("Signing key", provenance?.signingKeyId)}${technicalFact("Runtime image", provenance?.runtimeImage)}${provenance?.buildImage ? technicalFact("Build image", provenance.buildImage) : ""}</dl></details>`;
  }

  function updateDeploymentDiagnosis(deployment) {
    const status = diagnosisContent.querySelector("[data-deployment-status]");
    if (!status) {
      renderDeploymentDiagnosis();
      return;
    }
    status.className = `status-badge status-${statusClass(deployment.status)}`;
    status.textContent = capitalize(deployment.status);
    diagnosisContent.querySelector("[data-deployment-duration]").textContent = deploymentDuration(deployment);
    const focused = document.activeElement;
    const actions = diagnosisContent.querySelector("[data-deployment-actions]");
    if (!actions.contains(focused)) actions.innerHTML = deploymentActions(deployment);
    const body = diagnosisContent.querySelector(".diagnosis-body");
    if (!body.contains(focused)) body.innerHTML = deploymentDiagnosisBody(deployment);
  }

  function renderDeploymentSteps(steps) {
    if (!steps.length) return `<section class="deployment-steps"><div class="deployment-steps-heading"><h3>Promotion steps</h3><span>Waiting for worker</span></div><p class="muted-value">Diagnostics will appear as validation begins.</p></section>`;
    return `<section class="deployment-steps" aria-labelledby="deployment-steps-title"><div class="deployment-steps-heading"><h3 id="deployment-steps-title">Promotion steps</h3><span>${steps.length} recorded</span></div><div class="step-list">${steps.map((step) => `<article class="step-row"><div class="step-heading"><span class="status-dot ${step.status === "succeeded" ? "is-healthy" : step.status === "failed" ? "is-failed" : "is-running"}" aria-hidden="true"></span><strong>${escapeHTML(step.id === "validate" || step.id === "build" || step.id === "activate" ? capitalize(step.id) : step.id.replace(/^check:/, "Check · "))}</strong><span class="status-badge status-${statusClass(step.status)}">${escapeHTML(capitalize(step.status))}</span><time class="tabular">${escapeHTML(stepDuration(step))}</time></div>${step.error ? `<p class="step-error">${escapeHTML(step.error)}</p>` : ""}${step.logs ? `<pre class="step-logs">${escapeHTML(step.logs)}</pre>` : ""}</article>`).join("")}</div></section>`;
  }

  function selectDiagnosisTab(tab, moveFocus = true) {
    state.diagnosisTab = tab;
    if (state.selectedRun) writeDiagnosisRoute("run", state.selectedRun.id);
    renderDiagnosis();
    if (moveFocus) diagnosisContent.querySelector(`[data-diagnosis-tab="${tab}"]`)?.focus();
  }

  function diagnosisTabContent(run) {
    if (state.diagnosisTab === "journey") {
      const approvals = state.approvals.filter((approval) => approval.automationId === run.automationId);
      const journey = renderActorJourney(run, approvals, "diagnosis-journey");
      return journey || `<section class="journey-empty"><h3>Single execution boundary</h3><p>This run has no recorded human or timed continuation. Its exact event and execution timestamps remain available below.</p>${renderFlowLedger({activeRevisionId: run.revisionId, enabled: true}, null, [], run)}</section>`;
    }
    if (state.diagnosisTab === "logs") {
      return `<div class="copy-row"><button class="button button-quiet" type="button" data-copy="logs">${icon("copy")}Copy logs</button></div><pre class="code-block">${escapeHTML(run.logs || "This run produced no stdout or stderr output.")}</pre>`;
    }
    if (state.diagnosisTab === "output") {
      return `<div class="copy-row"><button class="button button-quiet" type="button" data-copy="output">${icon("copy")}Copy output</button></div><pre class="code-block">${escapeHTML(prettyJSON(run.result) || "This run produced no structured result.")}</pre>`;
    }
    return `<dl class="diagnosis-facts"><dt>Automation</dt><dd>${escapeHTML(run.automationId)}</dd><dt>Created</dt><dd class="tabular">${escapeHTML(formatDate(run.createdAt))}</dd><dt>Started</dt><dd class="tabular">${escapeHTML(formatDate(run.startedAt))}</dd><dt>Finished</dt><dd class="tabular">${escapeHTML(formatDate(run.finishedAt))}</dd><dt>Attempts</dt><dd>${escapeHTML(`${run.attempt} of ${run.maxAttempts}`)}</dd></dl><details class="technical-disclosure"><summary>Technical details</summary><dl class="technical-grid">${technicalFact("Run", run.id)}${technicalFact("Revision", run.revisionId)}${technicalFact("Event", run.eventId)}</dl></details>`;
  }

  function renderRunFailure(run) {
    if (run.status !== "failed") return "";
    const error = run.error || "Werkt recorded a failed terminal state without an error message.";
    return `<section class="run-failure" aria-labelledby="run-failure-title"><h3 id="run-failure-title">Run failed on attempt ${escapeHTML(run.attempt)} of ${escapeHTML(run.maxAttempts)}</h3><p>${escapeHTML(error)}</p><div class="run-failure-actions"><button class="button button-primary" type="button" data-diagnosis-tab-action="logs" aria-keyshortcuts="L">Open logs</button><button class="button button-quiet" type="button" data-copy-diagnostic>${icon("copy")}Copy diagnostic bundle</button><button class="button button-quiet" type="button" data-queue-diagnostic="${escapeHTML(run.automationId)}">${icon("play")}Queue diagnostic run</button><button class="section-link" type="button" data-open-help="help-recovery">Recovery guide</button></div></section>`;
  }

  function diagnosticBundle(run) {
    return JSON.stringify({
      runId: run.id,
      automationId: run.automationId,
      revisionId: run.revisionId,
      eventId: run.eventId,
      status: run.status,
      attempt: run.attempt,
      maxAttempts: run.maxAttempts,
      createdAt: run.createdAt,
      startedAt: run.startedAt || null,
      finishedAt: run.finishedAt || null,
      error: run.error || null,
      logs: run.logs || "",
      output: run.result ?? null,
    }, null, 2);
  }

  async function toggleAutomation(enabled) {
    if (!state.detail || state.mutating) return;
    const automationID = state.detail.id;
    state.mutating = true;
    renderAutomationDetail();
    try {
      const response = await updateAutomationEnabled(automationID, enabled);
      showReceipt(
        enabled ? "Automation resumed" : "Automation paused",
        response.changed
          ? (enabled
            ? `${automationID} is active. Future schedules were recalculated; missed intervals were not replayed.`
            : `${automationID} ingress stopped. Queued and running jobs continue.`)
          : `No change — ${automationID} was already ${enabled ? "active" : "paused"}.`,
        {audit: response.changed, resumeAutomationId: !enabled && response.changed ? automationID : ""},
      );
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(error.message, true);
    } finally {
      state.mutating = false;
      render();
    }
  }

  async function resumeAutomationFromReceipt(automationID, receipt) {
    if (!automationID || state.mutating) return;
    state.mutating = true;
    receipt.querySelectorAll("button").forEach((button) => { button.disabled = true; });
    try {
      const response = await updateAutomationEnabled(automationID, true);
      receipt.remove();
      showReceipt(
        response.changed ? "Pause reversed" : "Automation already active",
        response.changed
          ? `${automationID} is active. Future schedules were recalculated; missed intervals were not replayed.`
          : `No change — ${automationID} was already active.`,
        {audit: response.changed},
      );
    } catch (error) {
      receipt.querySelectorAll("button").forEach((button) => { button.disabled = false; });
      if (!(error instanceof AuthenticationRequired)) showToast(error.message, true);
    } finally {
      state.mutating = false;
      render();
    }
  }

  async function updateAutomationEnabled(automationID, enabled) {
    const response = await api(`/api/v1/automations/${encodeURIComponent(automationID)}`, {
      method: "PATCH",
      headers: {"Content-Type": "application/json"},
      body: JSON.stringify({enabled}),
    });
    if (state.selectedAutomation === automationID) state.detail = response.automation;
    const summary = state.automations.find((item) => item.id === automationID);
    if (summary) Object.assign(summary, response.automation);
    return response;
  }

  async function openManualRun(automationID = state.detail?.id, sourceRunID = "") {
    if (!automationID) return;
    runError.textContent = "";
    runPayload.removeAttribute("aria-invalid");
    let detail = state.detail?.id === automationID ? state.detail : null;
    try {
      if (!detail) detail = await api(`/api/v1/automations/${encodeURIComponent(automationID)}`);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(`Run target could not be loaded: ${error.message}`, true);
      return;
    }
    state.pendingManualRun = {
      automationId: automationID,
      expectedRevisionId: detail.activeRevisionId,
      sourceRunId: sourceRunID,
    };
    runTargetAutomation.textContent = automationID;
    runTargetRevision.textContent = detail.activeRevisionId;
    const activeRevision = detail.revisions?.find((revision) => revision.active) || detail.revisions?.[0];
    runTargetVersion.textContent = activeRevision?.createdAt
      ? `Deployed ${relativeTime(activeRevision.createdAt)}${activeRevision.provenance ? " · verified" : ""}`
      : "Current deployed version";
    runTargetLifecycle.textContent = detail.enabled ? "Active" : "Paused · manual runs remain available";
    const scope = currentScope();
    runTargetEnvironment.textContent = scope.environment;
    runTargetInstance.textContent = scope.instance;
    runTargetActor.textContent = scope.actor;
    runPayload.value = sourceRunID
      ? `{\n  "reason": "diagnose ${sourceRunID}"\n}`
      : '{\n  "reason": "operator diagnostic"\n}';
    runIdempotency.textContent = `workspace-${automationID}-${Date.now()}`;
    runDialog.showModal();
    runForm.querySelector('button[type="submit"]')?.focus();
  }

  async function queueManualRun() {
    const target = state.pendingManualRun;
    if (!target || state.mutating) return;
    let parsed;
    try {
      parsed = JSON.parse(runPayload.value);
    } catch (_) {
      runError.textContent = "Event data must be valid JSON before a run can be queued.";
      runPayload.setAttribute("aria-invalid", "true");
      runPayload.focus();
      return;
    }
    runError.textContent = "";
    runPayload.removeAttribute("aria-invalid");
    const submitButton = runForm.querySelector('button[type="submit"]');
    submitButton.disabled = true;
    state.mutating = true;
    try {
      const current = await api(`/api/v1/automations/${encodeURIComponent(target.automationId)}`);
      if (current.activeRevisionId !== target.expectedRevisionId) {
        target.expectedRevisionId = current.activeRevisionId;
        runTargetRevision.textContent = current.activeRevisionId;
        const activeRevision = current.revisions?.find((revision) => revision.active) || current.revisions?.[0];
        runTargetVersion.textContent = activeRevision?.createdAt
          ? `Deployed ${relativeTime(activeRevision.createdAt)}${activeRevision.provenance ? " · verified" : ""}`
          : "Current deployed version";
        runTargetLifecycle.textContent = current.enabled ? "Active" : "Paused · manual runs remain available";
        runError.textContent = "The deployed version changed while this dialog was open. Werkt updated the target; review it, then run the diagnostic again.";
        runError.focus();
        return;
      }
      const response = await api(`/api/v1/automations/${encodeURIComponent(target.automationId)}/runs`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": runIdempotency.textContent.trim(),
          "X-Werkt-Expected-Revision": target.expectedRevisionId,
        },
        body: JSON.stringify(parsed),
      });
      runDialog.close();
      showReceipt(
        response.created ? "Diagnostic run queued" : "Existing diagnostic opened",
        response.created
          ? `${target.automationId} is starting against the deployed version you reviewed.`
          : `${target.automationId} already received this diagnostic request; Werkt opened the existing run.`,
        {audit: true},
      );
      state.pendingManualRun = null;
      await loadWorkspace();
      await openRun(response.runId);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) {
        runError.textContent = error.status === 409
          ? "The active revision changed while this request was being queued. Review the current automation, then open the manual run form again."
          : error.message;
      }
    } finally {
      state.mutating = false;
      submitButton.disabled = false;
      render();
    }
  }

  async function openApproval(approvalID) {
    if (state.mutating || approvalDialog.open) return;
    approvalError.textContent = "";
    try {
      const approval = await api(`/api/v1/approvals/${encodeURIComponent(approvalID)}`);
      if (approval.status !== "pending") {
        showToast(`This approval is already ${approval.status}. Refresh the inbox to see its outcome.`, true);
        await refreshFeed("approvals");
        return;
      }
      state.selectedApproval = {...approval, idempotencyKey: `workspace-approval-${approval.id}-${Date.now()}`};
      approvalTitle.textContent = approval.title;
      approvalDescription.textContent = approval.description || "Review the requested fields before choosing an action.";
      approvalScopeAutomation.textContent = approval.automationId;
      approvalScopeRevision.textContent = approval.revisionId;
      approvalScopeRequested.textContent = formatDate(approval.createdAt);
      approvalScopeExpires.textContent = `${formatDate(approval.expiresAt)} · ${relativeTime(approval.expiresAt)}`;
      approvalFields.innerHTML = approval.fields.map(renderApprovalField).join("");
      approvalActions.innerHTML = `<button class="button button-quiet" type="button" data-close-dialog="approval">Cancel</button>${approval.actions.map((action) => `<button class="button ${action.style === "primary" ? "button-primary" : action.style === "danger" ? "button-danger" : "button-quiet"}" type="button" data-resolve-approval="${escapeHTML(action.id)}">${escapeHTML(action.label)}</button>`).join("")}`;
      approvalDialog.showModal();
      approvalDialog.querySelector("input, textarea, select, [data-resolve-approval]")?.focus();
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(`Approval could not be loaded. ${recoveryGuidance(error)}`, true);
    }
  }

  function renderApprovalField(field) {
    const controlID = `approval-field-${field.id}`;
    const descriptionID = `approval-field-${field.id}-description`;
    const describedBy = field.description ? ` aria-describedby="${escapeHTML(descriptionID)}"` : "";
    let control = "";
    if (field.type === "textarea") control = `<textarea id="${escapeHTML(controlID)}" rows="5" data-approval-field="${escapeHTML(field.id)}"${describedBy}>${escapeHTML(field.value || "")}</textarea>`;
    else if (field.type === "select") control = `<select id="${escapeHTML(controlID)}" data-approval-field="${escapeHTML(field.id)}"${describedBy}><option value="">Choose an option</option>${(field.options || []).map((option) => `<option value="${escapeHTML(option)}"${field.value === option ? " selected" : ""}>${escapeHTML(option)}</option>`).join("")}</select>`;
    else if (field.type === "boolean") control = `<label class="approval-checkbox"><input id="${escapeHTML(controlID)}" type="checkbox" data-approval-field="${escapeHTML(field.id)}"${field.value ? " checked" : ""}><span>${escapeHTML(field.label)}${field.required ? ' <span class="required-mark">Required</span>' : ""}</span></label>`;
    else control = `<input id="${escapeHTML(controlID)}" type="${field.type === "number" ? "number" : "text"}" data-approval-field="${escapeHTML(field.id)}" value="${escapeHTML(field.value ?? "")}"${describedBy}>`;
    const visibleLabel = field.type === "boolean" ? "" : `<label for="${escapeHTML(controlID)}">${escapeHTML(field.label)}${field.required ? ' <span class="required-mark">Required</span>' : ""}</label>`;
    return `<div class="approval-field" data-field-required="${field.required ? "true" : "false"}">${visibleLabel}${control}${field.description ? `<p id="${escapeHTML(descriptionID)}" class="field-help">${escapeHTML(field.description)}</p>` : ""}</div>`;
  }

  function approvalFieldValues() {
    const values = {};
    approvalFields.querySelectorAll("[data-approval-field]").forEach((control) => {
      if (control.type === "checkbox") values[control.dataset.approvalField] = control.checked;
      else if (control.type === "number" && control.value !== "") values[control.dataset.approvalField] = Number(control.value);
      else if (control.value !== "") values[control.dataset.approvalField] = control.value;
    });
    return values;
  }

  async function resolveApproval(actionID) {
    const approval = state.selectedApproval;
    if (!approval || state.mutating) return;
    const action = approval.actions.find((item) => item.id === actionID);
    if (!action) return;
    const controls = [...approvalFields.querySelectorAll("[data-approval-field]")];
    controls.forEach((control) => {
      const wrapper = control.closest("[data-field-required]");
      control.required = Boolean(action.requiresFields && wrapper?.dataset.fieldRequired === "true");
    });
    if (!approvalForm.reportValidity()) return;
    approvalError.textContent = "";
    state.mutating = true;
    approvalActions.querySelectorAll("button").forEach((button) => { button.disabled = true; });
    try {
      const response = await api(`/api/v1/approvals/${encodeURIComponent(approval.id)}/actions`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": approval.idempotencyKey,
          "X-Werkt-Expected-Revision": approval.revisionId,
        },
        body: JSON.stringify({action: actionID, fields: approvalFieldValues()}),
      });
      approvalDialog.close();
      state.selectedApproval = null;
      showReceipt(
        actionID === "approve" ? "Approval accepted" : "Approval rejected",
        `${approval.automationId} will resume safely against the same deployed version.`,
        {audit: true},
      );
      await refreshFeed("approvals");
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) approvalError.textContent = recoveryGuidance(error);
    } finally {
      state.mutating = false;
      approvalActions.querySelectorAll("button").forEach((button) => { button.disabled = false; });
    }
  }

  function openAction(action) {
    state.pendingAction = action;
    actionError.textContent = "";
    const scope = currentScope();
    actionScopeEnvironment.textContent = scope.environment;
    actionScopeInstance.textContent = scope.instance;
    actionScopeActor.textContent = actorLabel(scope.actor);
    actionScopeActor.title = scope.actor;
    if (action.kind === "rollback") {
      actionIconUse.setAttribute("href", "#icon-rollback");
      const version = revisionDisplayName(action.revisionId);
      actionTitle.textContent = `Restore ${version}?`;
      actionMessage.textContent = `Werkt will make the ${version} active for ${action.automationId} and restore its triggers. Work already running stays on the version it started with.`;
      actionSubmit.textContent = "Restore version";
      actionDismiss.textContent = "Keep current version";
    } else if (action.kind === "pause") {
      actionIconUse.setAttribute("href", "#icon-pause");
      actionTitle.textContent = `Pause ${action.automationId}?`;
      actionMessage.textContent = "New trigger events will stop. Work already queued or running will finish on the version it started with. Diagnostic runs remain available, and missed schedules are not replayed after resume.";
      actionSubmit.textContent = "Pause automation";
      actionDismiss.textContent = "Keep active";
    } else {
      actionIconUse.setAttribute("href", "#icon-stop");
      actionTitle.textContent = "Cancel deployment?";
      actionMessage.textContent = "Werkt will stop this deployment at its current promotion step. Its package and diagnostics remain available for a retry.";
      actionSubmit.textContent = "Cancel deployment";
      actionDismiss.textContent = "Keep deployment running";
    }
    actionDialog.showModal();
    actionDismiss.focus();
  }

  async function executeAction() {
    if (!state.pendingAction || state.mutating) return;
    const action = state.pendingAction;
    state.mutating = true;
    actionSubmit.disabled = true;
    actionError.textContent = "";
    try {
      if (action.kind === "rollback") {
        await api(`/api/v1/automations/${encodeURIComponent(action.automationId)}/rollback`, {
          method: "POST",
          headers: {"Content-Type": "application/json"},
          body: JSON.stringify({revisionId: action.revisionId}),
        });
        actionDialog.close();
        showReceipt("Version restored", `${action.automationId} now uses the version you selected. Work already running was not changed.`, {audit: true});
        await loadWorkspace({preserveSelection: true});
      } else if (action.kind === "pause") {
        const response = await updateAutomationEnabled(action.automationId, false);
        actionDialog.close();
        showReceipt(
          response.changed ? "Automation paused" : "Automation already paused",
          response.changed
            ? `${action.automationId} ingress stopped. Queued and running jobs continue.`
            : `No change — ${action.automationId} was already paused.`,
          {audit: response.changed, resumeAutomationId: response.changed ? action.automationId : ""},
        );
      } else {
        const deployment = await api(`/api/v1/deployments/${encodeURIComponent(action.deploymentId)}/cancel`, {method: "POST"});
        actionDialog.close();
        state.selectedDeployment = deployment;
        showReceipt(
          deployment.status === "cancelled" ? "Deployment cancelled" : "Deployment cancellation requested",
          `${deployment.automationId || "The package"} ${deployment.status === "cancelled" ? "stopped" : "is stopping at its current promotion step"}. Its source and diagnostics remain available.`,
          {audit: true},
        );
        await refreshDeploymentLists();
        renderDeploymentDiagnosis();
      }
      state.pendingAction = null;
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) actionError.textContent = error.message;
    } finally {
      state.mutating = false;
      actionSubmit.disabled = false;
      render();
      if (state.selectedDeployment) renderDeploymentDiagnosis();
    }
  }

  async function retryDeployment(deploymentID) {
    if (state.mutating) return;
    state.mutating = true;
    renderDeploymentDiagnosis();
    try {
      const response = await api(`/api/v1/deployments/${encodeURIComponent(deploymentID)}/retry`, {
        method: "POST",
        headers: {"Idempotency-Key": `workspace-retry-${deploymentID}`},
      });
      showReceipt(
        response.created ? "Retry queued" : "Existing retry opened",
        response.created
          ? `${response.deployment.automationId || "The automation"} is retrying the retained package.`
          : "Werkt opened the existing retry for this package.",
        {audit: response.created},
      );
      await refreshDeploymentLists();
      await openDeployment(response.deployment.id);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(error.message, true);
    } finally {
      state.mutating = false;
      if (state.selectedDeployment) renderDeploymentDiagnosis();
    }
  }

  async function refreshDeploymentLists() {
    const page = await api(`/api/v1/deployments?limit=${HISTORY_LIMIT}`, {page: true});
    state.deployments = page.items;
    state.feedNextCursor.deployments = page.nextCursor;
    if (state.view === "deployments") renderGlobalDeployments();
  }

  async function refreshFeed(kind) {
    const paths = {
      approvals: `/api/v1/approvals?limit=${HISTORY_LIMIT}`,
      runs: `/api/v1/runs?limit=${HISTORY_LIMIT}`,
      deployments: `/api/v1/deployments?limit=${HISTORY_LIMIT}`,
      audit: `/api/v1/audit?limit=${HISTORY_LIMIT}`,
    };
    const request = ++feedRequests[kind];
    feedControllers[kind]?.abort();
    feedControllers[kind] = new AbortController();
    state.feedLoading[kind] = true;
    render();
    try {
      const page = await api(paths[kind], {signal: feedControllers[kind].signal, page: true});
      if (request !== feedRequests[kind]) return;
      state[kind] = page.items;
      state.feedNextCursor[kind] = page.nextCursor;
      state.feedErrors[kind] = "";
      showToast(`${capitalize(kind)} refreshed.`);
    } catch (error) {
      if (request !== feedRequests[kind] || isAbort(error)) return;
      if (!(error instanceof AuthenticationRequired)) state.feedErrors[kind] = feedMessage(kind, error);
    }
    if (request !== feedRequests[kind]) return;
    state.feedLoading[kind] = false;
    render();
  }

  async function loadMore(kind) {
    const cursor = state.feedNextCursor[kind];
    if (!cursor || state.feedLoading[kind]) return;
    const paths = {
      approvals: "/api/v1/approvals",
      runs: "/api/v1/runs",
      deployments: "/api/v1/deployments",
      audit: "/api/v1/audit",
    };
    state.feedLoading[kind] = true;
    render();
    try {
      const page = await api(`${paths[kind]}?limit=${HISTORY_LIMIT}&cursor=${encodeURIComponent(cursor)}`, {page: true});
      const known = new Set(state[kind].map((item) => item.id));
      state[kind].push(...page.items.filter((item) => !known.has(item.id)));
      state.feedNextCursor[kind] = page.nextCursor;
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(`Older ${kind} could not be loaded. ${recoveryGuidance(error)}`, true);
    } finally {
      state.feedLoading[kind] = false;
      render();
    }
  }

  async function refreshDetailRuns() {
    const automationID = state.selectedAutomation;
    if (!automationID) return;
    const request = ++detailRunsRequest;
    detailRunsController?.abort();
    detailRunsController = new AbortController();
    try {
      const page = await api(`/api/v1/runs?automation=${encodeURIComponent(automationID)}&limit=${HISTORY_LIMIT}`, {signal: detailRunsController.signal, page: true});
      if (request !== detailRunsRequest || automationID !== state.selectedAutomation) return;
      state.detailRuns = page.items;
      state.detailRunsError = "";
    } catch (error) {
      if (request !== detailRunsRequest || automationID !== state.selectedAutomation || isAbort(error)) return;
      if (!(error instanceof AuthenticationRequired)) state.detailRunsError = detailRunsMessage(error);
    }
    if (request !== detailRunsRequest || automationID !== state.selectedAutomation) return;
    render();
  }

  function openConnection(message = "") {
    if (connectionDialog.open) {
      if (message && !connectionError.textContent) {
        connectionError.textContent = message;
        tokenInput.toggleAttribute("aria-invalid", !state.auth.configured || Boolean(state.token));
      }
      return;
    }
    connectionError.textContent = message;
    tokenInput.toggleAttribute("aria-invalid", Boolean(message) && (!state.auth.configured || Boolean(state.token)));
    tokenInput.value = state.token;
    renderConnectionAuth();
    if (!connectionDialog.open) connectionDialog.showModal();
    if (state.auth.configured && !state.auth.authenticated && !state.token) oidcLoginButton.focus();
    else if (state.auth.authenticated && !state.token) oidcLogoutButton.focus();
    else tokenInput.focus();
  }

  function showToast(message, error = false) {
    const toast = document.createElement("div");
    toast.className = `toast${error ? " is-error" : ""}`;
    toast.textContent = message;
    toastRegion.append(toast);
    window.setTimeout(() => toast.remove(), 4200);
  }

  function showReceipt(title, message, {audit = false, resumeAutomationId = ""} = {}) {
    const receipt = document.createElement("article");
    receipt.className = "receipt";
    receipt.innerHTML = `<div><strong>${escapeHTML(title)}</strong><p>${escapeHTML(message)}</p></div><div class="receipt-actions">${resumeAutomationId ? `<button class="button button-quiet button-compact" type="button" data-receipt-resume="${escapeHTML(resumeAutomationId)}">Resume</button>` : ""}${audit ? '<button class="button button-quiet button-compact" type="button" data-receipt-view="audit">View audit</button>' : ""}<button class="icon-button" type="button" data-dismiss-receipt aria-label="Dismiss receipt">${icon("close")}</button></div>`;
    receiptRegion.prepend(receipt);
    [...receiptRegion.children].slice(3).forEach((item) => item.remove());
  }

  function renderFatalError(error) {
    workspaceLoading.hidden = true;
    workspaceContent.hidden = false;
    workspaceContent.innerHTML = `<section class="error-state"><div class="error-state-inner"><h2>Workspace unavailable</h2><p>${escapeHTML(recoveryGuidance(error))}</p><button class="button button-primary" type="button" data-retry>Try again</button></div></section>`;
    shell.classList.add("has-fatal-error");
    inventoryCount.textContent = "Workspace unavailable";
    setConnection("error", "Unavailable");
  }

  function renderDetailError(error) {
    workspaceContent.innerHTML = `<section class="error-state"><div class="error-state-inner"><h2>Automation could not be loaded</h2><p>${escapeHTML(recoveryGuidance(error))}</p><button class="button button-primary" type="button" data-retry-detail>Try this automation again</button></div></section>`;
  }

  function historyScope(count) {
    if (count >= HISTORY_LIMIT) return `${count} records loaded`;
    return `${count} ${count === 1 ? "record" : "records"}`;
  }

  function feedNotice(kind) {
    if (state.feedLoading[kind]) return `<div class="inline-notice" role="status"><span>Refreshing ${kind}…</span></div>`;
    const message = state.feedErrors[kind];
    if (!message) return "";
    return `<div class="inline-notice is-error" role="status"><span>${escapeHTML(message)}</span><button class="button button-quiet button-compact" type="button" data-retry-feed="${kind}">Try again</button></div>`;
  }

  function statusClass(status) {
    if (["validating", "building", "checking", "activating"].includes(status)) return "running";
    return ["queued", "running", "succeeded", "failed", "cancelled"].includes(status) ? status : "queued";
  }

  function capitalize(value) {
    const text = String(value || "");
    return text ? text[0].toUpperCase() + text.slice(1) : "";
  }

  function shortID(value, length = 16) {
    const text = String(value || "—");
    return text.length > length ? `${text.slice(0, length)}…` : text;
  }

  function formatDate(value) {
    if (!value) return "Not yet";
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return String(value);
    return new Intl.DateTimeFormat(undefined, {dateStyle: "medium", timeStyle: "short"}).format(date);
  }

  function relativeTime(value) {
    if (!value) return "Not yet";
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return String(value);
    const deltaSeconds = Math.round((date.valueOf() - Date.now()) / 1000);
    const absolute = Math.abs(deltaSeconds);
    let divisor = 1;
    let unit = "second";
    if (absolute >= 86400) { divisor = 86400; unit = "day"; }
    else if (absolute >= 3600) { divisor = 3600; unit = "hour"; }
    else if (absolute >= 60) { divisor = 60; unit = "minute"; }
    return new Intl.RelativeTimeFormat(undefined, {numeric: "auto"}).format(Math.round(deltaSeconds / divisor), unit);
  }

  function relativeTimeElement(value) {
    if (!value) return "Not yet";
    const date = new Date(value);
    if (Number.isNaN(date.valueOf())) return escapeHTML(value);
    const iso = date.toISOString();
    return `<time datetime="${iso}" data-relative-time="${iso}" title="${escapeHTML(formatDate(iso))}">${escapeHTML(relativeTime(iso))}</time>`;
  }

  function refreshTemporalValues() {
    if (document.hidden) return;
    document.querySelectorAll("[data-relative-time]").forEach((element) => {
      element.textContent = relativeTime(element.dataset.relativeTime);
    });
  }

  function runDuration(run) {
    if (!run.startedAt) return "Not started";
    const start = new Date(run.startedAt).valueOf();
    const end = run.finishedAt ? new Date(run.finishedAt).valueOf() : Date.now();
    if (!Number.isFinite(start) || !Number.isFinite(end)) return "—";
    const milliseconds = Math.max(0, end - start);
    if (milliseconds < 1000) return `${milliseconds}ms`;
    if (milliseconds < 60000) return `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 1 : 0)}s`;
    return `${Math.floor(milliseconds / 60000)}m ${Math.round((milliseconds % 60000) / 1000)}s`;
  }

  function deploymentDuration(deployment) {
    const start = new Date(deployment.createdAt).valueOf();
    const end = deployment.finishedAt ? new Date(deployment.finishedAt).valueOf() : Date.now();
    if (!Number.isFinite(start) || !Number.isFinite(end)) return "—";
    const milliseconds = Math.max(0, end - start);
    if (milliseconds < 1000) return `${milliseconds}ms`;
    if (milliseconds < 60000) return `${(milliseconds / 1000).toFixed(milliseconds < 10000 ? 1 : 0)}s`;
    return `${Math.floor(milliseconds / 60000)}m ${Math.round((milliseconds % 60000) / 1000)}s`;
  }

  function stepDuration(step) {
    if (!step.startedAt) {
      if (step.status === "succeeded") return "Completed";
      if (step.status === "failed") return "Failed before start was recorded";
      if (step.status === "cancelled") return "Cancelled before start";
      return "Not started";
    }
    return deploymentDuration({createdAt: step.startedAt, finishedAt: step.finishedAt});
  }

  function prettyJSON(value) {
    if (value === undefined || value === null || value === "") return "";
    try {
      const parsed = typeof value === "string" ? JSON.parse(value) : value;
      return JSON.stringify(parsed, null, 2);
    } catch (_) {
      return String(value);
    }
  }

  function humanizeAction(action) {
    const values = {
      "automation.deployed": "Automation deployed",
      "deployment.succeeded": "Deployment succeeded",
      "deployment.failed": "Deployment failed",
      "deployment.cancel_requested": "Deployment cancellation requested",
      "automation.rolled_back": "Automation rolled back",
      "automation.paused": "Automation paused",
      "automation.resumed": "Automation resumed",
      "run.queued_manually": "Diagnostic run queued",
    };
    return values[action] || String(action || "Activity").replaceAll(".", " ");
  }

  function setMobileSearch(open) {
    globalSearch.classList.toggle("is-open", open);
    mobileSearchButton.setAttribute("aria-expanded", String(open));
    if (open) searchInput.focus();
  }

  function closeMobileDetail() {
    const previousSelection = state.selectedAutomation;
    state.selectedAutomation = "";
    state.detail = null;
    state.detailRuns = [];
    state.detailRunsError = "";
    shell.classList.remove("has-selection");
    writeRoute("push");
    render();
    inventoryList.querySelector(`[data-automation="${CSS.escape(previousSelection)}"]`)?.focus();
  }

  function openHelp(topic = "") {
    if (helpDialog.open || connectionDialog.open || runDialog.open || actionDialog.open || approvalDialog.open) return;
    helpDialog.showModal();
    const heading = typeof topic === "string" && topic ? helpDialog.querySelector(`#${CSS.escape(topic)}`) : null;
    (heading || helpDialog.querySelector("button"))?.focus();
  }

  document.addEventListener("click", (event) => {
    if (globalSearch.classList.contains("is-open") && !globalSearch.contains(event.target) && !mobileSearchButton.contains(event.target)) setMobileSearch(false);
    const helpTopic = event.target.closest("[data-open-help]");
    if (helpTopic) { openHelp(helpTopic.dataset.openHelp); return; }
    const viewButton = event.target.closest("[data-view], [data-view-link]");
    if (viewButton) {
      state.view = viewButton.dataset.view || viewButton.dataset.viewLink;
      closeDiagnosis();
      writeRoute("push");
      render();
      document.querySelector("#workspace-main")?.scrollTo({top: 0});
      document.querySelector("#workspace-main")?.focus();
      return;
    }
    const automationButton = event.target.closest("[data-automation]");
    if (automationButton) {
      state.view = "automations";
      selectAutomation(automationButton.dataset.automation);
      return;
    }
    const filterButton = event.target.closest("[data-enabled-filter]");
    if (filterButton) {
      state.enabledFilter = filterButton.dataset.enabledFilter;
      writeRoute();
      renderInventory();
      return;
    }
    const runLogsButton = event.target.closest("[data-run-logs]");
    if (runLogsButton) { openRun(runLogsButton.dataset.runLogs, "logs"); return; }
    const runButton = event.target.closest("[data-run]");
    if (runButton) { openRun(runButton.dataset.run); return; }
    const deploymentButton = event.target.closest("[data-deployment]");
    if (deploymentButton) { openDeployment(deploymentButton.dataset.deployment); return; }
    const auditButton = event.target.closest("[data-audit]");
    if (auditButton) { openAudit(auditButton.dataset.audit); return; }
    const approvalButton = event.target.closest("[data-approval]");
    if (approvalButton) { openApproval(approvalButton.dataset.approval); return; }
    const approvalAction = event.target.closest("[data-resolve-approval]");
    if (approvalAction) { resolveApproval(approvalAction.dataset.resolveApproval); return; }
    const cancelDeploymentButton = event.target.closest("[data-cancel-deployment]");
    if (cancelDeploymentButton) { openAction({kind: "cancel", deploymentId: cancelDeploymentButton.dataset.cancelDeployment}); return; }
    const retryDeploymentButton = event.target.closest("[data-retry-deployment]");
    if (retryDeploymentButton) { retryDeployment(retryDeploymentButton.dataset.retryDeployment); return; }
    const rollbackButton = event.target.closest("[data-rollback-revision]");
    if (rollbackButton && state.detail) { openAction({kind: "rollback", automationId: state.detail.id, revisionId: rollbackButton.dataset.rollbackRevision}); return; }
    if (event.target.closest("[data-close-diagnosis]")) { closeDiagnosis(); return; }
    const diagnosisTab = event.target.closest("[data-diagnosis-tab]");
    if (diagnosisTab) { selectDiagnosisTab(diagnosisTab.dataset.diagnosisTab); return; }
    const diagnosisTabAction = event.target.closest("[data-diagnosis-tab-action]");
    if (diagnosisTabAction) { selectDiagnosisTab(diagnosisTabAction.dataset.diagnosisTabAction); return; }
    const automationTab = event.target.closest("[data-automation-tab]");
    if (automationTab) { selectAutomationTab(automationTab.dataset.automationTab); return; }
    const flowLayer = event.target.closest("[data-flow-layer]");
    if (flowLayer) {
      state.flowLayer = flowLayer.dataset.flowLayer;
      if (state.flowLayer === "live") state.flowRunID = state.flowRunID || state.detailRuns[0]?.id || "";
      renderAutomationDetail();
      if (state.flowLayer === "live") loadFlowRun(state.flowRunID);
      workspaceContent.querySelector(`[data-flow-layer="${state.flowLayer}"]`)?.focus();
      return;
    }
    const flowEvidence = event.target.closest("[data-flow-evidence]");
    if (flowEvidence) {
      state.flowEvidence = flowEvidence.dataset.flowEvidence;
      renderAutomationDetail();
      workspaceContent.querySelector(`[data-flow-evidence="${CSS.escape(state.flowEvidence)}"]`)?.focus();
      return;
    }
    const retryFlowRun = event.target.closest("[data-retry-flow-run]");
    if (retryFlowRun) { loadFlowRun(retryFlowRun.dataset.retryFlowRun, {force: true, focusSelector: '[data-flow-focus="retry-run"]'}); return; }
    if (event.target.closest("[data-retry-detail-approvals]") && state.selectedAutomation) { retryFlowApprovals(); return; }
    if (event.target.closest("[data-copy-diagnostic]") && state.selectedRun) {
      navigator.clipboard.writeText(diagnosticBundle(state.selectedRun)).then(() => showToast("Diagnostic bundle copied."), () => showToast("Clipboard access was unavailable.", true));
      return;
    }
    const diagnosticRun = event.target.closest("[data-queue-diagnostic]");
    if (diagnosticRun && state.selectedRun) { openManualRun(diagnosticRun.dataset.queueDiagnostic, state.selectedRun.id); return; }
    const copyValue = event.target.closest("[data-copy-value]");
    if (copyValue) {
      navigator.clipboard.writeText(copyValue.dataset.copyValue || "").then(
        () => showToast("Copied to clipboard."),
        () => showToast("Clipboard access was unavailable.", true),
      );
      return;
    }
    const copyButton = event.target.closest("[data-copy]");
    if (copyButton && state.selectedRun) {
      const text = copyButton.dataset.copy === "logs" ? state.selectedRun.logs : prettyJSON(state.selectedRun.result);
      navigator.clipboard.writeText(text || "").then(() => showToast("Copied to clipboard."), () => showToast("Clipboard access was unavailable.", true));
      return;
    }
    const toggleButton = event.target.closest("[data-toggle-enabled]");
    if (toggleButton) {
      const enabled = toggleButton.dataset.toggleEnabled === "true";
      if (enabled) toggleAutomation(true);
      else if (state.detail) openAction({kind: "pause", automationId: state.detail.id});
      return;
    }
    if (event.target.closest("[data-open-run]")) { openManualRun(); return; }
    if (event.target.closest("[data-mobile-back]")) { closeMobileDetail(); return; }
    const closeDialog = event.target.closest("[data-close-dialog]");
    if (closeDialog) {
      if (closeDialog.dataset.closeDialog === "connection") connectionDialog.close();
      else if (closeDialog.dataset.closeDialog === "run") runDialog.close();
      else if (closeDialog.dataset.closeDialog === "approval") approvalDialog.close();
      else {
        actionDialog.close();
        state.pendingAction = null;
      }
      return;
    }
    if (event.target.closest("[data-retry]")) { loadWorkspace(); }
    const retryFeed = event.target.closest("[data-retry-feed]");
    if (retryFeed) { refreshFeed(retryFeed.dataset.retryFeed); return; }
    const loadMoreButton = event.target.closest("[data-load-more]");
    if (loadMoreButton) { loadMore(loadMoreButton.dataset.loadMore); return; }
    if (event.target.closest("[data-retry-detail]")) { loadAutomation(state.selectedAutomation); return; }
    if (event.target.closest("[data-retry-detail-runs]")) { refreshDetailRuns(); }
    if (event.target.closest("[data-dismiss-receipt]")) { event.target.closest(".receipt")?.remove(); return; }
    const receiptResume = event.target.closest("[data-receipt-resume]");
    if (receiptResume) {
      resumeAutomationFromReceipt(receiptResume.dataset.receiptResume, receiptResume.closest(".receipt"));
      return;
    }
    const receiptView = event.target.closest("[data-receipt-view]");
    if (receiptView) {
      state.view = receiptView.dataset.receiptView;
      closeDiagnosis();
      writeRoute("push");
      render();
      refreshFeed("audit");
      document.querySelector("#workspace-main")?.focus();
      return;
    }
  });

  document.querySelector("#refresh-button").addEventListener("click", () => loadWorkspace());
  document.querySelector("#mobile-refresh-button").addEventListener("click", () => loadWorkspace());
  mobileSearchButton.addEventListener("click", () => setMobileSearch(!globalSearch.classList.contains("is-open")));
  helpButton.addEventListener("click", () => openHelp());
  document.querySelector("#connection-button").addEventListener("click", () => openConnection());
  operatorScope.addEventListener("click", () => openConnection());
  oidcLoginButton.addEventListener("click", () => {
    state.token = "";
    writeToken("");
    window.location.assign("/api/v1/auth/login");
  });
  oidcLogoutButton.addEventListener("click", async () => {
    oidcLogoutButton.disabled = true;
    connectionError.textContent = "";
    try {
      const response = await fetch("/api/v1/auth/logout", {
        method: "POST",
        headers: {Accept: "application/json", "X-Werkt-CSRF": state.auth.csrfToken},
      });
      if (!response.ok) throw new Error("logout failed");
      state.auth = {...state.auth, authenticated: false, identity: null, csrfToken: ""};
      renderOperatorScope();
      renderConnectionAuth();
      connectionDialog.close();
      await loadWorkspace({preserveSelection: true});
    } catch (_) {
      connectionError.textContent = "Could not sign out. Refresh the page and try again.";
    } finally {
      oidcLogoutButton.disabled = false;
    }
  });
  document.querySelector("#clear-token-button").addEventListener("click", () => {
    state.token = "";
    writeToken("");
    tokenInput.value = "";
    tokenInput.removeAttribute("aria-invalid");
    renderConnectionAuth();
    renderOperatorScope();
    connectionDialog.close();
    loadWorkspace({preserveSelection: true});
  });

  searchInput.addEventListener("input", () => {
    state.query = searchInput.value.trim();
    writeRoute();
    renderInventory();
  });

  document.addEventListener("change", (event) => {
    if (event.target.matches("[data-flow-run]")) {
      state.flowRunID = event.target.value;
      state.flowEvidence = "revision";
      loadFlowRun(state.flowRunID, {focusSelector: "[data-flow-run]"});
      return;
    }
    if (event.target.id === "run-status-filter") {
      state.runStatusFilter = event.target.value;
      writeRoute();
      renderGlobalRuns();
    }
    if (event.target.id === "deployment-status-filter") {
      state.deploymentStatusFilter = event.target.value;
      writeRoute();
      renderGlobalDeployments();
    }
    if (event.target.id === "approval-status-filter") {
      state.approvalStatusFilter = event.target.value;
      writeRoute();
      renderGlobalApprovals();
    }
  });

  connectionForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.token = tokenInput.value.trim();
    if (!state.token) {
      connectionError.textContent = "Enter a management token or continue with Authelia.";
      tokenInput.setAttribute("aria-invalid", "true");
      tokenInput.focus();
      return;
    }
    writeToken(state.token);
    renderOperatorScope();
    connectionError.textContent = "";
    tokenInput.removeAttribute("aria-invalid");
    connectionDialog.close();
    loadWorkspace({preserveSelection: true});
  });

  runForm.addEventListener("submit", (event) => {
    event.preventDefault();
    queueManualRun();
  });

  runDialog.addEventListener("close", () => {
    state.pendingManualRun = null;
    runError.textContent = "";
    runPayload.removeAttribute("aria-invalid");
    runIdempotency.removeAttribute("aria-invalid");
  });

  actionDialog.addEventListener("close", () => {
    if (!state.mutating) state.pendingAction = null;
  });

  actionForm.addEventListener("submit", (event) => {
    event.preventDefault();
    executeAction();
  });

  approvalForm.addEventListener("submit", (event) => event.preventDefault());
  approvalDialog.addEventListener("close", () => {
    if (!state.mutating) state.selectedApproval = null;
    approvalError.textContent = "";
  });

  document.addEventListener("keydown", (event) => {
    const typing = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement || event.target?.isContentEditable;
    const nativeDialogOpen = connectionDialog.open || runDialog.open || actionDialog.open || approvalDialog.open || helpDialog.open;
    if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k" && !nativeDialogOpen) {
      event.preventDefault();
      setMobileSearch(true);
      return;
    }
    if (event.key === "?" && !typing && !nativeDialogOpen) {
      event.preventDefault();
      openHelp();
      return;
    }
    if (event.key.toLocaleLowerCase() === "l" && !event.metaKey && !event.ctrlKey && !event.altKey && !typing && !nativeDialogOpen && state.selectedRun) {
      event.preventDefault();
      selectDiagnosisTab("logs");
      return;
    }
    if (event.key.toLocaleLowerCase() === "r" && event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey && !typing && !nativeDialogOpen && diagnosisPane.hidden) {
      event.preventDefault();
      loadWorkspace({preserveSelection: true});
      return;
    }
    if (event.key.toLocaleLowerCase() === "r" && !event.shiftKey && !event.metaKey && !event.ctrlKey && !event.altKey && !typing && !nativeDialogOpen && diagnosisPane.hidden && state.detail) {
      event.preventDefault();
      openManualRun();
      return;
    }
    if (event.altKey && !event.metaKey && !event.ctrlKey && ["1", "2", "3", "4", "5"].includes(event.key) && !nativeDialogOpen && diagnosisPane.hidden) {
      event.preventDefault();
      document.querySelectorAll("[data-view]")[Number(event.key) - 1]?.click();
      return;
    }
    const inventoryItem = event.target.closest?.(".inventory-item");
    if (inventoryItem && ["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      const items = [...inventoryList.querySelectorAll(".inventory-item")];
      const current = items.indexOf(inventoryItem);
      const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : (current + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
      items.forEach((item, index) => { item.tabIndex = index === next ? 0 : -1; });
      items[next]?.focus();
      return;
    }
    const diagnosisTab = event.target.closest?.("[data-diagnosis-tab]");
    if (diagnosisTab && ["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      const tabs = ["summary", "journey", "logs", "output"];
      const current = tabs.indexOf(diagnosisTab.dataset.diagnosisTab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
      selectDiagnosisTab(tabs[next]);
      return;
    }
    const automationTab = event.target.closest?.("[data-automation-tab]");
    if (automationTab && ["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      const tabs = ["overview", "flow"];
      const current = tabs.indexOf(automationTab.dataset.automationTab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
      selectAutomationTab(tabs[next]);
      return;
    }
    if (event.key === "Tab" && !diagnosisPane.hidden && diagnosisModalQuery.matches && !nativeDialogOpen) {
      const focusable = [...diagnosisPane.querySelectorAll('button:not([disabled]), [href], input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])')].filter((element) => !element.hidden && element.getClientRects().length);
      if (focusable.length) {
        const first = focusable[0];
        const last = focusable[focusable.length - 1];
        if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
      }
    }
    if (event.key === "Escape" && globalSearch.classList.contains("is-open")) {
      event.preventDefault();
      setMobileSearch(false);
      mobileSearchButton.focus();
      return;
    }
    if (event.key === "Escape" && diagnosisPane.hidden === false && !nativeDialogOpen) closeDiagnosis();
  });

  diagnosisModalQuery.addEventListener("change", syncDiagnosisModality);

  window.addEventListener("popstate", applyRouteState);

  async function pollSelectedDeployment() {
    const deployment = state.selectedDeployment;
    if (deployment && !state.mutating && !["succeeded", "failed", "cancelled"].includes(deployment.status)) {
      const request = ++deploymentPollRequest;
      deploymentPollController = new AbortController();
      try {
        const refreshed = await api(`/api/v1/deployments/${encodeURIComponent(deployment.id)}`, {signal: deploymentPollController.signal});
        if (request === deploymentPollRequest && !diagnosisPane.hidden && !state.selectedRun && state.selectedDeployment?.id === deployment.id) {
          state.selectedDeployment = refreshed;
          const summary = state.deployments.find((item) => item.id === deployment.id);
          if (summary) Object.assign(summary, refreshed);
          updateDeploymentDiagnosis(refreshed);
          if (state.view === "deployments") renderGlobalDeployments();
        }
      } catch (error) {
        if (request === deploymentPollRequest && !isAbort(error) && !(error instanceof AuthenticationRequired)) {
          showToast(`Deployment refresh failed. ${recoveryGuidance(error)}`, true);
        }
      }
    }
    window.setTimeout(pollSelectedDeployment, 2000);
  }

  window.setTimeout(pollSelectedDeployment, 2000);

  async function pollSelectedRun() {
    const run = state.selectedRun;
    if (run && !state.mutating && !["succeeded", "failed"].includes(run.status)) {
      const request = ++runPollRequest;
      runPollController = new AbortController();
      try {
        const refreshed = await api(`/api/v1/runs/${encodeURIComponent(run.id)}`, {signal: runPollController.signal});
        if (request === runPollRequest && !diagnosisPane.hidden && !state.selectedDeployment && state.selectedRun?.id === run.id) {
          state.selectedRun = refreshed;
          const summary = state.runs.find((item) => item.id === run.id);
          if (summary) Object.assign(summary, refreshed);
          const detailSummary = state.detailRuns.find((item) => item.id === run.id);
          if (detailSummary) Object.assign(detailSummary, refreshed);
          updateRunDiagnosis(refreshed);
          if (state.view === "runs") renderGlobalRuns();
        }
      } catch (error) {
        if (request === runPollRequest && !isAbort(error) && !(error instanceof AuthenticationRequired)) {
          showToast(`Run refresh failed. ${recoveryGuidance(error)}`, true);
        }
      }
    }
    window.setTimeout(pollSelectedRun, 2000);
  }

  window.setTimeout(pollSelectedRun, 2000);

  async function pollFlowRun() {
    const runID = state.automationTab === "flow" && state.flowLayer === "live" ? state.flowRunID : "";
    const run = runID ? state.flowRunDetails[runID] : null;
    if (run && !state.mutating && !["succeeded", "failed"].includes(run.status)) {
      const request = ++flowPollRequest;
      flowPollController?.abort();
      flowPollController = new AbortController();
      try {
        const refreshed = await api(`/api/v1/runs/${encodeURIComponent(runID)}`, {signal: flowPollController.signal});
        if (request === flowPollRequest && state.automationTab === "flow" && state.flowLayer === "live" && state.flowRunID === runID) {
          const becameTerminal = !["succeeded", "failed"].includes(run.status) && ["succeeded", "failed"].includes(refreshed.status);
          state.flowRunDetails[runID] = refreshed;
          const summary = state.detailRuns.find((item) => item.id === runID);
          if (summary) Object.assign(summary, refreshed);
          if (becameTerminal && state.detail) {
            try {
              const approvals = await api(`/api/v1/approvals?automation=${encodeURIComponent(state.detail.id)}&limit=${HISTORY_LIMIT}`, {page: true});
              state.detailApprovals = approvals.items;
              state.detailApprovalsError = "";
            } catch (error) {
              if (!isAbort(error) && !(error instanceof AuthenticationRequired)) state.detailApprovalsError = detailApprovalsMessage(error);
            }
          }
          renderAutomationDetailPreservingFocus();
        }
      } catch (error) {
        if (request === flowPollRequest && !isAbort(error) && !(error instanceof AuthenticationRequired)) {
          state.flowRunErrors[runID] = `Live run refresh failed. ${recoveryGuidance(error)}`;
          renderAutomationDetailPreservingFocus();
        }
      }
    }
    window.setTimeout(pollFlowRun, 2000);
  }

  window.setTimeout(pollFlowRun, 2000);

  window.setInterval(refreshTemporalValues, 30000);
  document.addEventListener("visibilitychange", refreshTemporalValues);

  Object.assign(state, readRoute());
  searchInput.value = state.query;
  (async () => {
    const authError = new URLSearchParams(location.search).get("auth_error");
    await refreshBrowserSession();
    if (authError) {
      const messages = {
        cancelled: "Authelia sign-in was cancelled.",
        expired: "The sign-in attempt expired. Start again to continue.",
        invalid_flow: "The sign-in response could not be verified. Start again to continue.",
        provider_unavailable: "Authelia is temporarily unavailable. Try again in a moment.",
        not_authorized: "This Authelia account is not authorized for Werkt.",
        session_failed: "Werkt could not create a secure session. Try again.",
        not_configured: "Authelia sign-in is not configured on this Werkt server.",
      };
      openConnection(messages[authError] || "Authelia sign-in did not complete. Try again.");
    }
    loadWorkspace({preserveSelection: false});
  })();
})();
