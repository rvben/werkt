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
  const searchInput = document.querySelector("#automation-search");
  const globalSearch = document.querySelector("#global-search-control");
  const mobileSearchButton = document.querySelector("#mobile-search-button");
  const helpButton = document.querySelector("#help-button");
  const helpDialog = document.querySelector("#help-dialog");
  const connectionDialog = document.querySelector("#connection-dialog");
  const connectionForm = document.querySelector("#connection-form");
  const connectionError = document.querySelector("#connection-error");
  const tokenInput = document.querySelector("#management-token");
  const runDialog = document.querySelector("#run-dialog");
  const runForm = document.querySelector("#run-form");
  const runError = document.querySelector("#run-error");
  const runPayload = document.querySelector("#run-payload");
  const runIdempotency = document.querySelector("#run-idempotency");
  const runTargetAutomation = document.querySelector("#run-target-automation");
  const runTargetRevision = document.querySelector("#run-target-revision");
  const runTargetLifecycle = document.querySelector("#run-target-lifecycle");
  const actionDialog = document.querySelector("#action-dialog");
  const actionForm = document.querySelector("#action-form");
  const actionTitle = document.querySelector("#action-dialog-title");
  const actionMessage = document.querySelector("#action-dialog-message");
  const actionError = document.querySelector("#action-error");
  const actionSubmit = document.querySelector("#action-submit");
  const actionDismiss = document.querySelector("#action-dismiss");
  const actionIconUse = document.querySelector("#action-icon-use");
  const receiptRegion = document.querySelector("#receipt-region");
  const toastRegion = document.querySelector("#toast-region");

  const HISTORY_LIMIT = 100;
  const views = new Set(["automations", "runs", "deployments", "audit"]);
  const enabledFilters = new Set(["all", "enabled", "disabled", "failed"]);
  const runStatuses = new Set(["all", "queued", "running", "succeeded", "failed"]);
  const deploymentStatuses = new Set(["all", "in-progress", "succeeded", "failed"]);
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
  const feedRequests = {runs: 0, deployments: 0, audit: 0};
  const feedControllers = {runs: null, deployments: null, audit: null};
  const diagnosisModalQuery = window.matchMedia("(max-width: 74rem)");

  class APIError extends Error {
    constructor(message, status) {
      super(message);
      this.status = status;
    }
  }

  class AuthenticationRequired extends Error {}

  const state = {
    token: readToken(),
    automations: [],
    runs: [],
    deployments: [],
    audit: [],
    detail: null,
    detailRuns: [],
    selectedAutomation: "",
    selectedRun: null,
    selectedDeployment: null,
    diagnosisReturnFocus: null,
    diagnosisTab: "summary",
    view: "automations",
    enabledFilter: "all",
    query: "",
    runStatusFilter: "all",
    deploymentStatusFilter: "all",
    loading: true,
    mutating: false,
    pendingAction: null,
    pendingManualRun: null,
    feedErrors: {runs: "", deployments: "", audit: ""},
    feedLoading: {runs: true, deployments: true, audit: true},
    detailRunsError: "",
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
      query: params.get("q") || "",
      selectedAutomation,
    };
  }

  function writeRoute(mode = "replace") {
    const url = new URL(location.href);
    const params = new URLSearchParams();
    if (state.view !== "automations") params.set("view", state.view);
    if (state.enabledFilter !== "all") params.set("enabled", state.enabledFilter);
    if (state.runStatusFilter !== "all") params.set("runStatus", state.runStatusFilter);
    if (state.deploymentStatusFilter !== "all") params.set("deploymentStatus", state.deploymentStatusFilter);
    if (state.query) params.set("q", state.query);
    url.search = params.toString();
    url.hash = state.selectedAutomation ? `/${encodeURIComponent(state.selectedAutomation)}` : "";
    history[mode === "push" ? "pushState" : "replaceState"](null, "", url);
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
    closeDiagnosis({restoreFocus: false});
    if (state.selectedAutomation && state.selectedAutomation !== previousSelection && state.automations.some((item) => item.id === state.selectedAutomation)) {
      loadAutomation(state.selectedAutomation);
      return;
    }
    render();
  }

  function isAbort(error) {
    return error?.name === "AbortError";
  }

  function feedMessage(kind, error) {
    const labels = {runs: "run history", deployments: "deployment history", audit: "audit history"};
    const retained = state[kind]?.length ? " Previous data remains visible." : "";
    return `${labels[kind]} could not be refreshed.${retained} ${recoveryGuidance(error)}`;
  }

  function detailRunsMessage(error) {
    const retained = state.detailRuns.length ? " Previous automation runs remain visible." : "";
    return `Run history for this automation could not be refreshed.${retained} ${recoveryGuidance(error)}`;
  }

  function recoveryGuidance(error) {
    if (error instanceof APIError) {
      if (error.status === 404) return "The management endpoint or requested resource was not found. Verify this server exposes the current management API, then retry.";
      if (error.status === 409) return "The resource changed while the request was in progress. Refresh and review the current state before retrying.";
      if (error.status >= 500) return "The management API reported a server error. Verify the server is healthy, then retry.";
      return "The management API rejected the request. Review Connection settings and retry.";
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
    headers.set("X-Werkt-Actor", "workspace:operator");
    if (state.token) headers.set("Authorization", `Bearer ${state.token}`);
    const response = await fetch(path, {...options, headers});
    const contentType = response.headers.get("Content-Type") || "";
    const body = contentType.includes("application/json") ? await response.json() : null;
    if (response.status === 401) {
      setConnection("error", "Authentication required");
      openConnection("Enter the management token configured for this Werkt server.");
      throw new AuthenticationRequired();
    }
    if (!response.ok) {
      throw new APIError(body?.error || `Request failed with status ${response.status}`, response.status);
    }
    return body;
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
      setConnection("connected", "Connected");

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
        api(`/api/v1/runs?limit=${HISTORY_LIMIT}`, {signal}),
        api(`/api/v1/deployments?limit=${HISTORY_LIMIT}`, {signal}),
        api(`/api/v1/audit?limit=${HISTORY_LIMIT}`, {signal}),
      ]);
      if (request !== workspaceRequest) return;
      for (const [index, kind] of ["runs", "deployments", "audit"].entries()) {
        if (feedRequests[kind] !== workspaceFeedRequests[kind]) continue;
        const result = feeds[index];
        state.feedLoading[kind] = false;
        if (result.status === "fulfilled") {
          state[kind] = result.value;
          state.feedErrors[kind] = "";
        } else if (!isAbort(result.reason)) {
          state.feedErrors[kind] = feedMessage(kind, result.reason);
        }
      }
      render();
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
    const [detailResult, runsResult] = await Promise.allSettled([
      api(`/api/v1/automations/${encoded}`, {signal}),
      api(`/api/v1/runs?automation=${encoded}&limit=${HISTORY_LIMIT}`, {signal}),
    ]);
    if (request !== detailRequest || automationID !== state.selectedAutomation) return;
    if (detailResult.status === "rejected") throw detailResult.reason;
    state.detail = detailResult.value;
    if (runsRequest !== detailRunsRequest) {
      // A newer explicit run-history refresh owns this surface.
    } else if (runsResult.status === "fulfilled") {
      state.detailRuns = runsResult.value;
      state.detailRunsError = "";
    } else if (!isAbort(runsResult.reason)) {
      state.detailRunsError = detailRunsMessage(runsResult.reason);
    }
    if (rerender || state.detail) render();
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
    const automations = filteredAutomations();
    const projectCount = new Set(automations.map((automation) => automation.project)).size;
    inventoryResults.textContent = state.enabledFilter === "failed"
      ? `Showing ${automations.length} ${automations.length === 1 ? "automation" : "automations"} needing attention in ${projectCount} ${projectCount === 1 ? "project" : "projects"}.`
      : `Showing ${automations.length} of ${state.automations.length} automations.`;
    if (!automations.length) {
      inventoryList.innerHTML = `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("search")}</span><h2>No matching automations</h2><p>Adjust the search or lifecycle filter to restore the inventory.</p></div></div>`;
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
      <span class="revision-pill mono">${escapeHTML(shortID(automation.activeRevisionId, 7))}</span>
    </button>`;
  }

  function renderCurrentView() {
    if (state.view === "runs") renderGlobalRuns();
    else if (state.view === "deployments") renderGlobalDeployments();
    else if (state.view === "audit") renderGlobalAudit();
    else if (!state.automations.length) renderEmptyWorkspace();
    else if (!state.selectedAutomation) renderSelectAutomation();
    else if (state.detail) renderAutomationDetail();
    else showDetailLoading();
  }

  function renderSelectAutomation() {
    workspaceContent.innerHTML = `<section class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("bolt")}</span><h2>Select an automation</h2><p>Choose an automation from the inventory to inspect its active revision, triggers, recent runs, and safe operator actions.</p></div></section>`;
  }

  function showDetailLoading() {
    workspaceContent.innerHTML = `<div class="workspace-loading" role="status"><span class="sr-only">Loading automation…</span><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-block"></div><div class="skeleton skeleton-block short"></div></div>`;
  }

  function renderEmptyWorkspace() {
    workspaceContent.innerHTML = `<section class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("bolt")}</span><h2>Deploy the first automation</h2><p>The workspace will organize deployed packages by project and folder. Validation happens before an immutable revision is created.</p><code class="empty-command">werkt deploy ./path/to/automation</code></div></section>`;
  }

  function renderAutomationDetail() {
    const detail = state.detail;
    const runtime = detail.manifest?.runtime || {};
    const execution = detail.manifest?.execution || {};
    const activeRevision = detail.revisions?.find((revision) => revision.active) || detail.revisions?.[0];
    const triggers = detail.triggers || [];
    const recentRuns = state.detailRuns.slice(0, 8);
    const status = detail.enabled ? "active" : "paused";
    workspaceContent.innerHTML = `<article class="detail-shell">
      <header class="detail-header">
        <button class="icon-button mobile-back" type="button" data-mobile-back aria-label="Back to automation inventory">${icon("arrow-left")}</button>
        <div class="breadcrumbs"><span>${escapeHTML(detail.project)}</span>${icon("chevron")}<span>${escapeHTML(detail.folder || "Unfiled")}</span>${icon("chevron")}<span>${escapeHTML(detail.id)}</span></div>
        <div class="detail-title-row">
          <div class="detail-title"><div class="detail-title-line"><h2>${escapeHTML(detail.id)}</h2><span class="status-badge status-${status}">${detail.enabled ? "Active" : "Paused"}</span></div><p>${escapeHTML(detail.description || "No description is set in this automation's manifest.")}</p></div>
          <div class="detail-actions">
            <button class="button button-quiet" type="button" data-toggle-enabled="${detail.enabled ? "false" : "true"}" ${state.mutating ? "disabled" : ""}>${icon(detail.enabled ? "pause" : "play")}${detail.enabled ? "Pause" : "Resume"}</button>
            <button class="button button-primary" type="button" data-open-run aria-keyshortcuts="R" ${state.mutating ? "disabled" : ""}>${icon("play")}Run now</button>
          </div>
        </div>
      </header>
      <div class="detail-body">
        <dl class="facts">
          <div class="fact"><dt>Active revision</dt><dd class="mono" title="${escapeHTML(detail.activeRevisionId)}">${escapeHTML(shortID(detail.activeRevisionId, 16))}</dd></div>
          <div class="fact"><dt>Runtime</dt><dd>${escapeHTML(runtime.language || "Executable")} · ${escapeHTML(runtime.image || "local process")}</dd></div>
          <div class="fact"><dt>Concurrency</dt><dd>${escapeHTML(execution.concurrency || "allow")} · ${escapeHTML(execution.timeout || "5m timeout")}</dd></div>
          <div class="fact"><dt>Last deployed</dt><dd class="tabular">${escapeHTML(formatDate(activeRevision?.createdAt || detail.updatedAt))}</dd></div>
        </dl>

        <section class="workspace-section" aria-labelledby="triggers-heading">
          <div class="section-heading"><div><h3 id="triggers-heading">Triggers</h3><p>${triggers.length} configured event ${triggers.length === 1 ? "source" : "sources"}</p></div></div>
          ${renderTriggers(triggers, detail.enabled)}
        </section>

        <section class="workspace-section" aria-labelledby="runs-heading">
          <div class="section-heading"><div><h3 id="runs-heading">Recent runs</h3><p>Newest attempts for this automation · ${escapeHTML(historyScope(state.detailRuns.length))}</p></div><button class="section-link" type="button" data-view-link="runs">Browse recent runs</button></div>
          ${state.detailRunsError ? `<div class="inline-notice is-error" role="status"><span>${escapeHTML(state.detailRunsError)}</span><button class="button button-quiet button-compact" type="button" data-retry-detail-runs>Try again</button></div>` : ""}
          ${renderRunsTable(recentRuns, true)}
        </section>

        <section class="workspace-section" aria-labelledby="revisions-heading">
          <div class="section-heading"><div><h3 id="revisions-heading">Latest revisions</h3><p>${Math.min(detail.revisions?.length || 0, 8)} most recent immutable revisions</p></div></div>
          ${renderRevisions(detail.revisions || [])}
        </section>
      </div>
    </article>`;
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
    if (trigger.type === "webhook") return `HMAC · ${config.signatureHeader || "X-Werkt-Signature"}`;
    if (trigger.type === "email") return "Bearer-authenticated RFC 5322";
    if (trigger.type === "ntfy") return `${config.server || "ntfy"}/${config.topic || "topic"}`;
    return "Event source";
  }

  function renderRunsTable(runs, compact = false) {
    if (!runs.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No runs yet</h2><p>Trigger this automation or queue a manual diagnostic run to see execution history.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="status-column">Status</th>${compact ? "" : "<th>Automation</th>"}<th>Started</th><th class="hide-tablet">Attempt</th><th class="wide">Run ID</th><th class="hide-tablet">Duration</th></tr></thead><tbody>${runs.map((run) => `<tr><td data-label="Status"><span class="status-badge status-${statusClass(run.status)}">${escapeHTML(capitalize(run.status))}</span></td>${compact ? "" : `<td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(run.automationId)}">${escapeHTML(run.automationId)}</button></td>`}<td data-label="Started" class="tabular">${relativeTimeElement(run.startedAt || run.createdAt)}</td><td data-label="Attempt" class="hide-tablet">${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</td><td data-label="Run"><button class="table-button" type="button" data-run="${escapeHTML(run.id)}" aria-label="Diagnose run ${escapeHTML(run.id)}"><span class="mono">${escapeHTML(shortID(run.id, compact ? 18 : 26))}</span></button></td><td data-label="Duration" class="hide-tablet tabular">${escapeHTML(runDuration(run))}</td></tr>`).join("")}</tbody></table>`;
  }

  function renderRevisions(revisions) {
    if (!revisions.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No revisions available</h2><p>Deploy the automation package to create its first immutable revision.</p></div></div>`;
    return `<div class="revision-list">${revisions.slice(0, 8).map((revision) => `<div class="revision-row"><strong class="mono">${escapeHTML(shortID(revision.id, 18))}${revision.active ? " · active" : ""}</strong><span class="mono" title="${escapeHTML(revision.contentHash)}">${escapeHTML(shortID(revision.contentHash, 16))}</span><span class="tabular">${escapeHTML(formatDate(revision.createdAt))}</span><span class="revision-action">${revision.active ? '<span class="muted-value">Current</span>' : `<button class="button button-quiet button-compact" type="button" data-rollback-revision="${escapeHTML(revision.id)}" ${state.mutating ? "disabled" : ""}>${icon("rollback")}Roll back</button>`}</span></div>`).join("")}</div>`;
  }

  function renderGlobalRuns() {
    const runs = state.runStatusFilter === "all" ? state.runs : state.runs.filter((run) => run.status === state.runStatusFilter);
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Runs</h1><p>Up to ${HISTORY_LIMIT} recent runs across every automation. ${escapeHTML(historyScope(state.runs.length))}. Open a run to inspect attempts, logs, and structured output.</p></div><div class="global-toolbar"><label class="sr-only" for="run-status-filter">Filter runs by status</label><select class="select-control" id="run-status-filter"><option value="all">All statuses</option>${["queued", "running", "succeeded", "failed"].map((status) => `<option value="${status}"${state.runStatusFilter === status ? " selected" : ""}>${capitalize(status)}</option>`).join("")}</select></div></header>${feedNotice("runs")}${state.feedLoading.runs && !runs.length ? "" : renderRunsTable(runs)}</section>`;
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
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Deployments</h1><p>Up to ${HISTORY_LIMIT} recent package promotions across every automation. ${escapeHTML(historyScope(state.deployments.length))}.${activeCount ? ` ${activeCount} ${activeCount === 1 ? "deployment is" : "deployments are"} still in progress.` : ""}</p></div><div class="global-toolbar"><label class="sr-only" for="deployment-status-filter">Filter deployments by status</label><select class="select-control" id="deployment-status-filter"><option value="all">All statuses</option>${["in-progress", "succeeded", "failed"].map((status) => `<option value="${status}"${state.deploymentStatusFilter === status ? " selected" : ""}>${status === "in-progress" ? "In progress" : status === "failed" ? "Failed or cancelled" : "Succeeded"}</option>`).join("")}</select></div></header>${feedNotice("deployments")}${state.feedLoading.deployments && !deployments.length ? "" : renderDeploymentsTable(deployments)}</section>`;
  }

  function renderDeploymentsTable(deployments) {
    if (!deployments.length) return `<div class="empty-state"><div class="empty-state-inner"><span class="empty-symbol">${icon("package")}</span><h2>No deployments found</h2><p>Upload an automation package with the CLI or adjust the status filter.</p><code class="empty-command">werkt deploy ./path/to/automation</code></div></div>`;
    return `<table class="data-table"><thead><tr><th class="status-column">Status</th><th>Automation</th><th>Received</th><th class="hide-tablet">Actor</th><th class="wide">Deployment ID</th><th class="hide-tablet">Duration</th><th class="action-column"><span class="sr-only">Open</span></th></tr></thead><tbody>${deployments.map((deployment) => `<tr><td data-label="Status"><span class="status-badge status-${statusClass(deployment.status)}">${escapeHTML(capitalize(deployment.status))}</span></td><td data-label="Automation">${deployment.automationId ? `<button class="table-button" type="button" data-automation="${escapeHTML(deployment.automationId)}">${escapeHTML(deployment.automationId)}</button>` : '<span class="muted-value">Awaiting manifest</span>'}</td><td data-label="Received" class="tabular">${relativeTimeElement(deployment.createdAt)}</td><td data-label="Actor" class="hide-tablet">${escapeHTML(deployment.actor)}</td><td data-label="Deployment"><button class="table-button" type="button" data-deployment="${escapeHTML(deployment.id)}"><span class="mono">${escapeHTML(shortID(deployment.id, 26))}</span></button></td><td data-label="Duration" class="hide-tablet tabular">${escapeHTML(deploymentDuration(deployment))}</td><td class="action-column"><button class="icon-button" type="button" data-deployment="${escapeHTML(deployment.id)}" aria-label="Inspect deployment ${escapeHTML(deployment.id)}">${icon("chevron")}</button></td></tr>`).join("")}</tbody></table>`;
  }

  function renderGlobalAudit() {
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Audit</h1><p>Up to ${HISTORY_LIMIT} recent lifecycle changes attributed through the management API. ${escapeHTML(historyScope(state.audit.length))}.</p></div></header>${feedNotice("audit")}${state.feedLoading.audit && !state.audit.length ? "" : renderAuditTable(state.audit)}</section>`;
  }

  function renderAuditTable(events) {
    if (!events.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No audit activity yet</h2><p>Deployments and management mutations will appear here with actor attribution.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="wide">Action</th><th>Automation</th><th>Actor</th><th>When</th></tr></thead><tbody>${events.map((event) => `<tr><td data-label="Action">${escapeHTML(humanizeAction(event.action))}</td><td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(event.automationId)}"><span class="mono">${escapeHTML(event.automationId)}</span></button></td><td data-label="Actor">${escapeHTML(event.actor)}</td><td data-label="When" class="tabular">${relativeTimeElement(event.createdAt)}</td></tr>`).join("")}</tbody></table>`;
  }

  async function openRun(runID) {
    const request = ++diagnosisRequest;
    diagnosisController?.abort();
    diagnosisController = new AbortController();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    renderDiagnosisLoading("Loading run diagnosis…");
    syncDiagnosisModality();
    try {
      state.selectedDeployment = null;
      const run = await api(`/api/v1/runs/${encodeURIComponent(runID)}`, {signal: diagnosisController.signal});
      if (request !== diagnosisRequest) return;
      state.selectedRun = run;
      state.diagnosisTab = "summary";
      renderDiagnosis(true);
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) {
        diagnosisContent.innerHTML = `<div class="error-state"><div class="error-state-inner"><h2>Run could not be loaded</h2><p>${escapeHTML(error.message)}</p><button class="button button-quiet" type="button" data-close-diagnosis>Close diagnosis</button></div></div>`;
      }
    }
  }

  async function openDeployment(deploymentID) {
    const request = ++diagnosisRequest;
    diagnosisController?.abort();
    diagnosisController = new AbortController();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    renderDiagnosisLoading("Loading deployment details…");
    syncDiagnosisModality();
    try {
      state.selectedRun = null;
      const deployment = await api(`/api/v1/deployments/${encodeURIComponent(deploymentID)}`, {signal: diagnosisController.signal});
      if (request !== diagnosisRequest) return;
      state.selectedDeployment = deployment;
      renderDeploymentDiagnosis(true);
    } catch (error) {
      if (!isAbort(error) && !(error instanceof AuthenticationRequired)) {
        diagnosisContent.innerHTML = `<div class="error-state"><div class="error-state-inner"><h2>Deployment could not be loaded</h2><p>${escapeHTML(error.message)}</p><button class="button button-quiet" type="button" data-close-diagnosis>Close details</button></div></div>`;
      }
    }
  }

  function closeDiagnosis({restoreFocus = true} = {}) {
    diagnosisRequest += 1;
    diagnosisController?.abort();
    deploymentPollRequest += 1;
    deploymentPollController?.abort();
    diagnosisPane.hidden = true;
    shell.classList.remove("has-diagnosis");
    syncDiagnosisModality();
    state.selectedRun = null;
    state.selectedDeployment = null;
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
    const tabs = ["summary", "logs", "output"];
    const activeTab = state.diagnosisTab;
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Run diagnosis</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close run diagnosis">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-${statusClass(run.status)}">${escapeHTML(capitalize(run.status))}</span><code title="${escapeHTML(run.id)}">${escapeHTML(run.id)}</code></div><p class="diagnosis-meta"><span>${escapeHTML(run.automationId)}</span><span>${escapeHTML(runDuration(run))}</span><span>attempt ${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</span></p><div class="diagnosis-tabs" role="tablist" aria-label="Run detail">${tabs.map((tab) => `<button id="diagnosis-tab-${tab}" class="tab-button" type="button" role="tab" data-diagnosis-tab="${tab}" aria-controls="diagnosis-panel-${tab}" aria-selected="${activeTab === tab}" tabindex="${activeTab === tab ? "0" : "-1"}">${capitalize(tab)}</button>`).join("")}</div></div><div class="diagnosis-body">${renderRunFailure(run)}${tabs.map((tab) => `<div id="diagnosis-panel-${tab}" role="tabpanel" aria-labelledby="diagnosis-tab-${tab}" tabindex="0" ${activeTab === tab ? "" : "hidden"}>${activeTab === tab ? diagnosisTabContent(run) : ""}</div>`).join("")}</div>`;
    if (focusPanel) diagnosisContent.querySelector("[data-close-diagnosis]").focus();
  }

  function renderDeploymentDiagnosis(focusPanel = false) {
    const deployment = state.selectedDeployment;
    if (!deployment) return;
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Deployment details</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close deployment details">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-${statusClass(deployment.status)}" data-deployment-status>${escapeHTML(capitalize(deployment.status))}</span><code title="${escapeHTML(deployment.id)}">${escapeHTML(deployment.id)}</code></div><p class="diagnosis-meta"><span>${escapeHTML(deployment.automationId || "Manifest not validated yet")}</span><span data-deployment-duration>${escapeHTML(deploymentDuration(deployment))}</span><span>${escapeHTML(deployment.actor)}</span></p><div data-deployment-actions>${deploymentActions(deployment)}</div></div><div class="diagnosis-body">${deploymentDiagnosisBody(deployment)}</div>`;
    if (focusPanel) diagnosisContent.querySelector("[data-close-diagnosis]").focus();
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
    return `${failure}${cancellation}${renderDeploymentSteps(deployment.steps || [])}<dl class="diagnosis-facts"><dt>Automation</dt><dd>${escapeHTML(deployment.automationId || "Awaiting manifest")}</dd><dt>Revision</dt><dd class="mono">${escapeHTML(deployment.revisionId || "Not activated")}</dd>${deployment.retryOf ? `<dt>Retry of</dt><dd class="mono">${escapeHTML(deployment.retryOf)}</dd>` : ""}<dt>Package SHA-256</dt><dd class="mono">${escapeHTML(deployment.packageDigest)}</dd><dt>Content hash</dt><dd class="mono">${escapeHTML(deployment.contentHash || "Not built")}</dd><dt>Received</dt><dd class="tabular">${escapeHTML(formatDate(deployment.createdAt))}</dd><dt>Started</dt><dd class="tabular">${escapeHTML(formatDate(deployment.startedAt))}</dd><dt>Finished</dt><dd class="tabular">${escapeHTML(formatDate(deployment.finishedAt))}</dd><dt>Updated</dt><dd class="tabular">${escapeHTML(formatDate(deployment.updatedAt))}</dd></dl>`;
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
    renderDiagnosis();
    if (moveFocus) diagnosisContent.querySelector(`[data-diagnosis-tab="${tab}"]`)?.focus();
  }

  function diagnosisTabContent(run) {
    if (state.diagnosisTab === "logs") {
      return `<div class="copy-row"><button class="button button-quiet" type="button" data-copy="logs">${icon("copy")}Copy logs</button></div><pre class="code-block">${escapeHTML(run.logs || "This run produced no stdout or stderr output.")}</pre>`;
    }
    if (state.diagnosisTab === "output") {
      return `<div class="copy-row"><button class="button button-quiet" type="button" data-copy="output">${icon("copy")}Copy output</button></div><pre class="code-block">${escapeHTML(prettyJSON(run.result) || "This run produced no structured result.")}</pre>`;
    }
    return `<dl class="diagnosis-facts"><dt>Automation</dt><dd>${escapeHTML(run.automationId)}</dd><dt>Revision</dt><dd class="mono">${escapeHTML(run.revisionId)}</dd><dt>Event</dt><dd class="mono">${escapeHTML(run.eventId)}</dd><dt>Created</dt><dd class="tabular">${escapeHTML(formatDate(run.createdAt))}</dd><dt>Started</dt><dd class="tabular">${escapeHTML(formatDate(run.startedAt))}</dd><dt>Finished</dt><dd class="tabular">${escapeHTML(formatDate(run.finishedAt))}</dd><dt>Attempts</dt><dd>${escapeHTML(`${run.attempt} of ${run.maxAttempts}`)}</dd></dl>`;
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
    runIdempotency.removeAttribute("aria-invalid");
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
    runTargetLifecycle.textContent = detail.enabled ? "Active" : "Paused · manual runs remain available";
    runPayload.value = sourceRunID
      ? `{\n  "reason": "diagnose ${sourceRunID}"\n}`
      : '{\n  "reason": "operator diagnostic"\n}';
    runIdempotency.value = `workspace-${automationID}-${Date.now()}`;
    runDialog.showModal();
    runPayload.focus();
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
        const previous = target.expectedRevisionId;
        target.expectedRevisionId = current.activeRevisionId;
        runTargetRevision.textContent = current.activeRevisionId;
        runTargetLifecycle.textContent = current.enabled ? "Active" : "Paused · manual runs remain available";
        runError.textContent = `Active revision changed from ${previous} to ${current.activeRevisionId}. Review the new target, then queue again.`;
        runTargetRevision.focus();
        return;
      }
      const response = await api(`/api/v1/automations/${encodeURIComponent(target.automationId)}/runs`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": runIdempotency.value.trim(),
          "X-Werkt-Expected-Revision": target.expectedRevisionId,
        },
        body: JSON.stringify(parsed),
      });
      runDialog.close();
      showReceipt(
        response.created ? "Manual run queued" : "Existing manual run opened",
        `${response.runId} targets ${target.expectedRevisionId} for ${target.automationId}.`,
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

  function openAction(action) {
    state.pendingAction = action;
    actionError.textContent = "";
    if (action.kind === "rollback") {
      actionIconUse.setAttribute("href", "#icon-rollback");
      actionTitle.textContent = "Roll back revision?";
      actionMessage.textContent = `Werkt will make ${action.revisionId} active for ${action.automationId} and replace its effective triggers. Running jobs keep their pinned revision.`;
      actionSubmit.textContent = "Roll back revision";
      actionDismiss.textContent = "Keep current revision";
    } else if (action.kind === "pause") {
      actionIconUse.setAttribute("href", "#icon-pause");
      actionTitle.textContent = `Pause ${action.automationId}?`;
      actionMessage.textContent = "Schedule, webhook, email, and ntfy ingestion will stop. Queued and running jobs continue on their pinned revisions. Manual diagnostic runs remain available. When resumed, missed schedule intervals are not replayed.";
      actionSubmit.textContent = "Pause automation";
      actionDismiss.textContent = "Keep active";
    } else {
      actionIconUse.setAttribute("href", "#icon-stop");
      actionTitle.textContent = "Cancel deployment?";
      actionMessage.textContent = `Werkt will stop ${action.deploymentId} at its current promotion step. Its source and diagnostics remain available for a retry.`;
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
        showReceipt("Revision activated", `${action.automationId} now uses ${action.revisionId}. Running jobs kept their pinned revisions.`, {audit: true});
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
          `${deployment.id} ${deployment.status === "cancelled" ? "stopped" : "is stopping at its current promotion step"}. Its source and diagnostics remain available.`,
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
        `${response.deployment.id} retries the retained package from ${deploymentID}.`,
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
    state.deployments = await api(`/api/v1/deployments?limit=${HISTORY_LIMIT}`);
    if (state.view === "deployments") renderGlobalDeployments();
  }

  async function refreshFeed(kind) {
    const paths = {
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
      const values = await api(paths[kind], {signal: feedControllers[kind].signal});
      if (request !== feedRequests[kind]) return;
      state[kind] = values;
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

  async function refreshDetailRuns() {
    const automationID = state.selectedAutomation;
    if (!automationID) return;
    const request = ++detailRunsRequest;
    detailRunsController?.abort();
    detailRunsController = new AbortController();
    try {
      const runs = await api(`/api/v1/runs?automation=${encodeURIComponent(automationID)}&limit=${HISTORY_LIMIT}`, {signal: detailRunsController.signal});
      if (request !== detailRunsRequest || automationID !== state.selectedAutomation) return;
      state.detailRuns = runs;
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
        tokenInput.setAttribute("aria-invalid", "true");
      }
      return;
    }
    connectionError.textContent = message;
    tokenInput.toggleAttribute("aria-invalid", Boolean(message));
    tokenInput.value = state.token;
    if (!connectionDialog.open) connectionDialog.showModal();
    tokenInput.focus();
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
    if (count >= HISTORY_LIMIT) return `Showing the latest ${HISTORY_LIMIT} records`;
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
    if (!step.startedAt) return "Not started";
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
      "run.queued_manually": "Manual run queued",
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
    if (helpDialog.open || connectionDialog.open || runDialog.open || actionDialog.open) return;
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
    const runButton = event.target.closest("[data-run]");
    if (runButton) { openRun(runButton.dataset.run); return; }
    const deploymentButton = event.target.closest("[data-deployment]");
    if (deploymentButton) { openDeployment(deploymentButton.dataset.deployment); return; }
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
    if (event.target.closest("[data-copy-diagnostic]") && state.selectedRun) {
      navigator.clipboard.writeText(diagnosticBundle(state.selectedRun)).then(() => showToast("Diagnostic bundle copied."), () => showToast("Clipboard access was unavailable.", true));
      return;
    }
    const diagnosticRun = event.target.closest("[data-queue-diagnostic]");
    if (diagnosticRun && state.selectedRun) { openManualRun(diagnosticRun.dataset.queueDiagnostic, state.selectedRun.id); return; }
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
      else {
        actionDialog.close();
        state.pendingAction = null;
      }
      return;
    }
    if (event.target.closest("[data-retry]")) { loadWorkspace(); }
    const retryFeed = event.target.closest("[data-retry-feed]");
    if (retryFeed) { refreshFeed(retryFeed.dataset.retryFeed); return; }
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
  document.querySelector("#clear-token-button").addEventListener("click", () => {
    state.token = "";
    writeToken("");
    tokenInput.value = "";
    tokenInput.removeAttribute("aria-invalid");
    connectionDialog.close();
    loadWorkspace({preserveSelection: true});
  });

  searchInput.addEventListener("input", () => {
    state.query = searchInput.value.trim();
    writeRoute();
    renderInventory();
  });

  document.addEventListener("change", (event) => {
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
  });

  connectionForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.token = tokenInput.value.trim();
    writeToken(state.token);
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

  document.addEventListener("keydown", (event) => {
    const typing = event.target instanceof HTMLInputElement || event.target instanceof HTMLTextAreaElement || event.target instanceof HTMLSelectElement || event.target?.isContentEditable;
    const nativeDialogOpen = connectionDialog.open || runDialog.open || actionDialog.open || helpDialog.open;
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
    if (event.altKey && !event.metaKey && !event.ctrlKey && ["1", "2", "3", "4"].includes(event.key) && !nativeDialogOpen && diagnosisPane.hidden) {
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
      const tabs = ["summary", "logs", "output"];
      const current = tabs.indexOf(diagnosisTab.dataset.diagnosisTab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
      selectDiagnosisTab(tabs[next]);
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

  window.setInterval(refreshTemporalValues, 30000);
  document.addEventListener("visibilitychange", refreshTemporalValues);

  Object.assign(state, readRoute());
  searchInput.value = state.query;
  loadWorkspace({preserveSelection: false});
})();
