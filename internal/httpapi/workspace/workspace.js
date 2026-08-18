(() => {
  "use strict";

  const shell = document.querySelector("#app-shell");
  const inventoryList = document.querySelector("#inventory-list");
  const inventoryCount = document.querySelector("#inventory-count");
  const workspaceLoading = document.querySelector("#workspace-loading");
  const workspaceContent = document.querySelector("#workspace-content");
  const diagnosisPane = document.querySelector("#diagnosis-pane");
  const diagnosisContent = document.querySelector("#diagnosis-content");
  const connectionState = document.querySelector("#connection-state");
  const searchInput = document.querySelector("#automation-search");
  const connectionDialog = document.querySelector("#connection-dialog");
  const connectionForm = document.querySelector("#connection-form");
  const connectionError = document.querySelector("#connection-error");
  const tokenInput = document.querySelector("#management-token");
  const runDialog = document.querySelector("#run-dialog");
  const runForm = document.querySelector("#run-form");
  const runError = document.querySelector("#run-error");
  const runPayload = document.querySelector("#run-payload");
  const runIdempotency = document.querySelector("#run-idempotency");
  const toastRegion = document.querySelector("#toast-region");

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
    audit: [],
    detail: null,
    detailRuns: [],
    detailAudit: [],
    selectedAutomation: "",
    selectedRun: null,
    diagnosisReturnFocus: null,
    diagnosisTab: "summary",
    view: "automations",
    enabledFilter: "all",
    query: "",
    runStatusFilter: "all",
    loading: true,
    mutating: false,
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
    connectionState.querySelector("span:last-child").textContent = label;
  }

  async function loadWorkspace({preserveSelection = true} = {}) {
    state.loading = true;
    workspaceLoading.hidden = false;
    workspaceContent.hidden = true;
    try {
      const [automations, runs, audit] = await Promise.all([
        api("/api/v1/automations"),
        api("/api/v1/runs?limit=100"),
        api("/api/v1/audit?limit=100"),
      ]);
      state.automations = automations;
      state.runs = runs;
      state.audit = audit;
      setConnection("connected", "Connected");

      const hashSelection = decodeURIComponent(location.hash.replace(/^#\/?/, ""));
      const candidate = preserveSelection ? (state.selectedAutomation || hashSelection) : hashSelection;
      state.selectedAutomation = automations.some((item) => item.id === candidate) ? candidate : (automations[0]?.id || "");
      if (state.selectedAutomation) await loadAutomation(state.selectedAutomation, false);
      else {
        state.detail = null;
        state.detailRuns = [];
        state.detailAudit = [];
      }
      render();
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) renderFatalError(error);
    } finally {
      state.loading = false;
      workspaceLoading.hidden = true;
      workspaceContent.hidden = false;
    }
  }

  async function loadAutomation(automationID, rerender = true) {
    const encoded = encodeURIComponent(automationID);
    const [detail, runs, audit] = await Promise.all([
      api(`/api/v1/automations/${encoded}`),
      api(`/api/v1/runs?automation=${encoded}&limit=100`),
      api(`/api/v1/audit?automation=${encoded}&limit=100`),
    ]);
    state.detail = detail;
    state.detailRuns = runs;
    state.detailAudit = audit;
    if (rerender) render();
  }

  async function selectAutomation(automationID) {
    if (!automationID || automationID === state.selectedAutomation) {
      shell.classList.add("has-selection");
      render();
      return;
    }
    state.selectedAutomation = automationID;
    state.detail = null;
    state.detailRuns = [];
    state.detailAudit = [];
    shell.classList.add("has-selection");
    history.replaceState(null, "", `#/${encodeURIComponent(automationID)}`);
    renderInventory();
    showDetailLoading();
    try {
      await loadAutomation(automationID);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) renderFatalError(error);
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

  function latestRunFor(automationID) {
    return state.runs.find((run) => run.automationId === automationID);
  }

  function automationHealth(automation) {
    if (!automation.enabled) return {className: "is-paused", label: "Paused"};
    const run = latestRunFor(automation.id);
    if (!run) return {className: "", label: "No runs yet"};
    if (run.status === "failed") return {className: "is-failed", label: "Latest run failed"};
    if (run.status === "running" || run.status === "queued") return {className: "is-running", label: `Latest run ${run.status}`};
    return {className: "is-healthy", label: "Latest run succeeded"};
  }

  function filteredAutomations() {
    const query = state.query.toLocaleLowerCase();
    return state.automations.filter((automation) => {
      if (state.enabledFilter === "enabled" && !automation.enabled) return false;
      if (state.enabledFilter === "disabled" && automation.enabled) return false;
      if (!query) return true;
      return [automation.id, automation.project, automation.folder, automation.description, ...(automation.labels || [])]
        .join(" ").toLocaleLowerCase().includes(query);
    });
  }

  function renderInventory() {
    document.querySelectorAll("[data-enabled-filter]").forEach((button) => {
      button.classList.toggle("is-active", button.dataset.enabledFilter === state.enabledFilter);
    });
    const automations = filteredAutomations();
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
      return `<section class="inventory-group"><div class="inventory-group-title"><span>${escapeHTML(project)}</span><span>${count}</span></div>${[...folders.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([folder, items]) => `<div class="inventory-folder"><div class="inventory-folder-label">${escapeHTML(folder)}</div>${items.sort((a, b) => a.id.localeCompare(b.id)).map(renderInventoryItem).join("")}</div>`).join("")}</section>`;
    }).join("");
  }

  function renderInventoryItem(automation) {
    const health = automationHealth(automation);
    const selected = automation.id === state.selectedAutomation;
    return `<button class="inventory-item${selected ? " is-selected" : ""}" type="button" data-automation="${escapeHTML(automation.id)}" aria-pressed="${selected}" aria-label="${escapeHTML(automation.id)}, ${health.label}">
      <span class="status-dot ${health.className}" aria-hidden="true"></span>
      <span><span class="inventory-name">${escapeHTML(automation.id)}</span><span class="inventory-meta">${escapeHTML((automation.labels || []).join(" · ") || automation.description || "No labels")}</span></span>
      <span class="revision-pill mono">${escapeHTML(shortID(automation.activeRevisionId, 7))}</span>
    </button>`;
  }

  function renderCurrentView() {
    if (state.view === "runs") renderGlobalRuns();
    else if (state.view === "audit") renderGlobalAudit();
    else if (!state.automations.length) renderEmptyWorkspace();
    else if (state.detail) renderAutomationDetail();
    else showDetailLoading();
  }

  function showDetailLoading() {
    workspaceContent.innerHTML = `<div class="workspace-loading" aria-label="Loading automation"><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-block"></div><div class="skeleton skeleton-block short"></div></div>`;
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
            <button class="button button-primary" type="button" data-open-run ${state.mutating ? "disabled" : ""}>${icon("play")}Run now</button>
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
          <div class="section-heading"><div><h3 id="runs-heading">Recent runs</h3><p>Newest attempts for this automation</p></div><button class="section-link" type="button" data-view-link="runs">View all runs</button></div>
          ${renderRunsTable(recentRuns, true)}
        </section>

        <section class="workspace-section" aria-labelledby="revisions-heading">
          <div class="section-heading"><div><h3 id="revisions-heading">Revisions</h3><p>Immutable deployment history</p></div></div>
          ${renderRevisions(detail.revisions || [])}
        </section>
      </div>
    </article>`;
  }

  function renderTriggers(triggers, automationEnabled) {
    if (!triggers.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No triggers configured</h2><p>Declare an event source in <span class="mono">automation.yaml</span> and deploy a new revision.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="wide">Trigger</th><th>Configuration</th><th class="hide-tablet">Next event</th><th class="status-column">State</th></tr></thead><tbody>${triggers.map((trigger) => {
      const effective = Boolean(automationEnabled && trigger.enabled);
      return `<tr><td data-label="Trigger"><span class="trigger-type">${icon(icons[trigger.type] || "code")}<span>${escapeHTML(trigger.id)}</span></span></td><td data-label="Config">${escapeHTML(triggerConfiguration(trigger))}</td><td data-label="Next" class="hide-tablet tabular">${escapeHTML(trigger.nextFireAt ? relativeTime(trigger.nextFireAt) : "On event")}</td><td data-label="State"><span class="status-badge status-${effective ? "active" : "paused"}">${effective ? "Enabled" : "Paused"}</span></td></tr>`;
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
    return `<table class="data-table"><thead><tr><th class="status-column">Status</th>${compact ? "" : "<th>Automation</th>"}<th>Started</th><th class="hide-tablet">Attempt</th><th class="wide">Run ID</th><th class="hide-tablet">Duration</th><th class="action-column"><span class="sr-only">Open</span></th></tr></thead><tbody>${runs.map((run) => `<tr><td data-label="Status"><span class="status-badge status-${statusClass(run.status)}">${escapeHTML(capitalize(run.status))}</span></td>${compact ? "" : `<td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(run.automationId)}">${escapeHTML(run.automationId)}</button></td>`}<td data-label="Started" class="tabular">${escapeHTML(relativeTime(run.startedAt || run.createdAt))}</td><td data-label="Attempt" class="hide-tablet">${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</td><td data-label="Run"><button class="table-button" type="button" data-run="${escapeHTML(run.id)}"><span class="mono">${escapeHTML(shortID(run.id, compact ? 18 : 26))}</span></button></td><td data-label="Duration" class="hide-tablet tabular">${escapeHTML(runDuration(run))}</td><td class="action-column"><button class="icon-button" type="button" data-run="${escapeHTML(run.id)}" aria-label="Diagnose run ${escapeHTML(run.id)}">${icon("chevron")}</button></td></tr>`).join("")}</tbody></table>`;
  }

  function renderRevisions(revisions) {
    if (!revisions.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No revisions available</h2><p>Deploy the automation package to create its first immutable revision.</p></div></div>`;
    return `<div class="revision-list">${revisions.slice(0, 8).map((revision) => `<div class="revision-row"><strong class="mono">${escapeHTML(shortID(revision.id, 18))}${revision.active ? " · active" : ""}</strong><span class="mono" title="${escapeHTML(revision.contentHash)}">${escapeHTML(shortID(revision.contentHash, 16))}</span><span class="tabular">${escapeHTML(formatDate(revision.createdAt))}</span></div>`).join("")}</div>`;
  }

  function renderGlobalRuns() {
    const runs = state.runStatusFilter === "all" ? state.runs : state.runs.filter((run) => run.status === state.runStatusFilter);
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Runs</h1><p>Execution history across every automation. Open a run to inspect attempts, logs, and structured output.</p></div><div class="global-toolbar"><label class="sr-only" for="run-status-filter">Filter runs by status</label><select class="select-control" id="run-status-filter"><option value="all">All statuses</option>${["queued", "running", "succeeded", "failed"].map((status) => `<option value="${status}"${state.runStatusFilter === status ? " selected" : ""}>${capitalize(status)}</option>`).join("")}</select></div></header>${renderRunsTable(runs)}</section>`;
  }

  function renderGlobalAudit() {
    workspaceContent.innerHTML = `<section class="global-view"><header class="global-view-header"><div><h1>Audit</h1><p>Lifecycle changes, deployments, and manual runs attributed through the management API.</p></div></header>${renderAuditTable(state.audit)}</section>`;
  }

  function renderAuditTable(events) {
    if (!events.length) return `<div class="empty-state"><div class="empty-state-inner"><h2>No audit activity yet</h2><p>Deployments and management mutations will appear here with actor attribution.</p></div></div>`;
    return `<table class="data-table"><thead><tr><th class="wide">Action</th><th>Automation</th><th>Actor</th><th>When</th></tr></thead><tbody>${events.map((event) => `<tr><td data-label="Action">${escapeHTML(humanizeAction(event.action))}</td><td data-label="Automation"><button class="table-button" type="button" data-automation="${escapeHTML(event.automationId)}"><span class="mono">${escapeHTML(event.automationId)}</span></button></td><td data-label="Actor">${escapeHTML(event.actor)}</td><td data-label="When" class="tabular">${escapeHTML(relativeTime(event.createdAt))}</td></tr>`).join("")}</tbody></table>`;
  }

  async function openRun(runID) {
    state.diagnosisReturnFocus = document.activeElement;
    diagnosisPane.hidden = false;
    shell.classList.add("has-diagnosis");
    diagnosisContent.innerHTML = `<div class="workspace-loading"><div class="skeleton skeleton-title"></div><div class="skeleton skeleton-line"></div><div class="skeleton skeleton-block"></div></div>`;
    try {
      state.selectedRun = await api(`/api/v1/runs/${encodeURIComponent(runID)}`);
      state.diagnosisTab = "summary";
      renderDiagnosis(true);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) {
        diagnosisContent.innerHTML = `<div class="error-state"><div class="error-state-inner"><h2>Run could not be loaded</h2><p>${escapeHTML(error.message)}</p><button class="button button-quiet" type="button" data-close-diagnosis>Close diagnosis</button></div></div>`;
      }
    }
  }

  function closeDiagnosis() {
    diagnosisPane.hidden = true;
    shell.classList.remove("has-diagnosis");
    state.selectedRun = null;
    const returnFocus = state.diagnosisReturnFocus;
    state.diagnosisReturnFocus = null;
    if (returnFocus?.isConnected) returnFocus.focus();
    else document.querySelector("#workspace-main")?.focus();
  }

  function renderDiagnosis(focusPanel = false) {
    const run = state.selectedRun;
    if (!run) return;
    const tabs = ["summary", "logs", "output"];
    diagnosisContent.innerHTML = `<div class="diagnosis-header"><div class="diagnosis-header-top"><h2 id="diagnosis-title">Run diagnosis</h2><button class="icon-button" type="button" data-close-diagnosis aria-label="Close run diagnosis">${icon("close")}</button></div><div class="diagnosis-run"><span class="status-badge status-${statusClass(run.status)}">${escapeHTML(capitalize(run.status))}</span><code title="${escapeHTML(run.id)}">${escapeHTML(run.id)}</code></div><p class="diagnosis-meta"><span>${escapeHTML(run.automationId)}</span><span>${escapeHTML(runDuration(run))}</span><span>attempt ${escapeHTML(`${run.attempt}/${run.maxAttempts}`)}</span></p><div class="diagnosis-tabs" role="tablist" aria-label="Run detail">${tabs.map((tab) => `<button class="tab-button" type="button" role="tab" data-diagnosis-tab="${tab}" aria-selected="${state.diagnosisTab === tab}" tabindex="${state.diagnosisTab === tab ? "0" : "-1"}">${capitalize(tab)}</button>`).join("")}</div></div><div class="diagnosis-body">${diagnosisTabContent(run)}</div>`;
    if (focusPanel) diagnosisContent.querySelector("[data-close-diagnosis]").focus();
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
    return `<dl class="diagnosis-facts"><dt>Automation</dt><dd>${escapeHTML(run.automationId)}</dd><dt>Revision</dt><dd class="mono">${escapeHTML(run.revisionId)}</dd><dt>Event</dt><dd class="mono">${escapeHTML(run.eventId)}</dd><dt>Created</dt><dd class="tabular">${escapeHTML(formatDate(run.createdAt))}</dd><dt>Started</dt><dd class="tabular">${escapeHTML(formatDate(run.startedAt))}</dd><dt>Finished</dt><dd class="tabular">${escapeHTML(formatDate(run.finishedAt))}</dd><dt>Attempts</dt><dd>${escapeHTML(`${run.attempt} of ${run.maxAttempts}`)}</dd><dt>Error</dt><dd>${escapeHTML(run.error || "None")}</dd></dl>`;
  }

  async function toggleAutomation(enabled) {
    if (!state.detail || state.mutating) return;
    state.mutating = true;
    renderAutomationDetail();
    try {
      const response = await api(`/api/v1/automations/${encodeURIComponent(state.detail.id)}`, {
        method: "PATCH",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify({enabled}),
      });
      state.detail = response.automation;
      const summary = state.automations.find((item) => item.id === state.detail.id);
      if (summary) Object.assign(summary, response.automation);
      showToast(`${state.detail.id} is now ${enabled ? "active" : "paused"}.`);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) showToast(error.message, true);
    } finally {
      state.mutating = false;
      render();
    }
  }

  function openManualRun() {
    if (!state.detail) return;
    runError.textContent = "";
    runPayload.value = '{\n  "reason": "operator diagnostic"\n}';
    runIdempotency.value = `workspace-${state.detail.id}-${Date.now()}`;
    runDialog.showModal();
    runPayload.focus();
  }

  async function queueManualRun() {
    if (!state.detail || state.mutating) return;
    let parsed;
    try {
      parsed = JSON.parse(runPayload.value);
    } catch (_) {
      runError.textContent = "Event data must be valid JSON before a run can be queued.";
      runPayload.focus();
      return;
    }
    runError.textContent = "";
    const submitButton = runForm.querySelector('button[type="submit"]');
    submitButton.disabled = true;
    state.mutating = true;
    try {
      const response = await api(`/api/v1/automations/${encodeURIComponent(state.detail.id)}/runs`, {
        method: "POST",
        headers: {"Content-Type": "application/json", "Idempotency-Key": runIdempotency.value.trim()},
        body: JSON.stringify(parsed),
      });
      runDialog.close();
      showToast(response.created ? "Manual run queued." : "That idempotency key already points to an existing run.");
      await loadWorkspace();
      await openRun(response.runId);
    } catch (error) {
      if (!(error instanceof AuthenticationRequired)) runError.textContent = error.message;
    } finally {
      state.mutating = false;
      submitButton.disabled = false;
      render();
    }
  }

  function openConnection(message = "") {
    connectionError.textContent = message;
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

  function renderFatalError(error) {
    workspaceLoading.hidden = true;
    workspaceContent.hidden = false;
    workspaceContent.innerHTML = `<section class="error-state"><div class="error-state-inner"><h2>Workspace unavailable</h2><p>${escapeHTML(error?.message || "Werkt could not load the management API.")}</p><button class="button button-primary" type="button" data-retry>Try again</button></div></section>`;
    setConnection("error", "Unavailable");
  }

  function statusClass(status) {
    return ["queued", "running", "succeeded", "failed"].includes(status) ? status : "queued";
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
      "automation.paused": "Automation paused",
      "automation.resumed": "Automation resumed",
      "run.queued_manually": "Manual run queued",
    };
    return values[action] || String(action || "Activity").replaceAll(".", " ");
  }

  document.addEventListener("click", (event) => {
    const viewButton = event.target.closest("[data-view], [data-view-link]");
    if (viewButton) {
      state.view = viewButton.dataset.view || viewButton.dataset.viewLink;
      closeDiagnosis();
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
      renderInventory();
      return;
    }
    const runButton = event.target.closest("[data-run]");
    if (runButton) { openRun(runButton.dataset.run); return; }
    if (event.target.closest("[data-close-diagnosis]")) { closeDiagnosis(); return; }
    const diagnosisTab = event.target.closest("[data-diagnosis-tab]");
    if (diagnosisTab) { selectDiagnosisTab(diagnosisTab.dataset.diagnosisTab); return; }
    const copyButton = event.target.closest("[data-copy]");
    if (copyButton && state.selectedRun) {
      const text = copyButton.dataset.copy === "logs" ? state.selectedRun.logs : prettyJSON(state.selectedRun.result);
      navigator.clipboard.writeText(text || "").then(() => showToast("Copied to clipboard."), () => showToast("Clipboard access was unavailable.", true));
      return;
    }
    const toggleButton = event.target.closest("[data-toggle-enabled]");
    if (toggleButton) { toggleAutomation(toggleButton.dataset.toggleEnabled === "true"); return; }
    if (event.target.closest("[data-open-run]")) { openManualRun(); return; }
    if (event.target.closest("[data-mobile-back]")) { shell.classList.remove("has-selection"); return; }
    const closeDialog = event.target.closest("[data-close-dialog]");
    if (closeDialog) {
      if (closeDialog.dataset.closeDialog === "connection") connectionDialog.close();
      else runDialog.close();
      return;
    }
    if (event.target.closest("[data-retry]")) { loadWorkspace(); }
  });

  document.querySelector("#refresh-button").addEventListener("click", () => loadWorkspace());
  document.querySelector("#mobile-refresh-button").addEventListener("click", () => loadWorkspace());
  document.querySelector("#mobile-search-button").addEventListener("click", () => {
    searchInput.closest(".global-search").classList.toggle("is-open");
    searchInput.focus();
  });
  document.querySelector("#connection-button").addEventListener("click", () => openConnection());
  document.querySelector("#clear-token-button").addEventListener("click", () => {
    state.token = "";
    writeToken("");
    tokenInput.value = "";
    connectionDialog.close();
    loadWorkspace({preserveSelection: true});
  });

  searchInput.addEventListener("input", () => {
    state.query = searchInput.value.trim();
    renderInventory();
  });

  document.addEventListener("change", (event) => {
    if (event.target.id === "run-status-filter") {
      state.runStatusFilter = event.target.value;
      renderGlobalRuns();
    }
  });

  connectionForm.addEventListener("submit", (event) => {
    event.preventDefault();
    state.token = tokenInput.value.trim();
    writeToken(state.token);
    connectionError.textContent = "";
    connectionDialog.close();
    loadWorkspace({preserveSelection: true});
  });

  runForm.addEventListener("submit", (event) => {
    event.preventDefault();
    queueManualRun();
  });

  document.addEventListener("keydown", (event) => {
    if ((event.metaKey || event.ctrlKey) && event.key.toLocaleLowerCase() === "k") {
      event.preventDefault();
      searchInput.closest(".global-search").classList.add("is-open");
      searchInput.focus();
    }
    const diagnosisTab = event.target.closest?.("[data-diagnosis-tab]");
    if (diagnosisTab && ["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) {
      event.preventDefault();
      const tabs = ["summary", "logs", "output"];
      const current = tabs.indexOf(diagnosisTab.dataset.diagnosisTab);
      const next = event.key === "Home" ? 0 : event.key === "End" ? tabs.length - 1 : (current + (event.key === "ArrowRight" ? 1 : -1) + tabs.length) % tabs.length;
      selectDiagnosisTab(tabs[next]);
    }
    if (event.key === "Escape" && diagnosisPane.hidden === false && !connectionDialog.open && !runDialog.open) closeDiagnosis();
  });

  loadWorkspace({preserveSelection: false});
})();
