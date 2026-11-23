(() => {
  "use strict";

  const form = document.getElementById("ask-form");
  const text = document.getElementById("text");
  const files = document.getElementById("files");
  const repo = document.getElementById("repo");
  const lane = document.getElementById("lane");
  const go = document.getElementById("go");
  const note = document.getElementById("form-note");
  const list = document.getElementById("requests");
  const repos = document.getElementById("repos");
  const card = document.getElementById("request-card");
  const repoRow = document.getElementById("repo-row");
  const who = document.getElementById("who");
  let me = { repositories: [], reads: [] };
  const open = new Set();

  // A link to one request is the page's address with #<id>: the request is
  // shown even when the list does not hold it, opened when the reader reads
  // it whole, and scrolled to once.
  let wanted = location.hash.slice(1);
  let arrived = false;
  window.addEventListener("hashchange", () => {
    wanted = location.hash.slice(1);
    arrived = false;
    refresh();
  });

  // The front door signs people in. A 401 from the API means the pass
  // expired: reload so the front door can sign the person in again. A second
  // 401 within a minute of a reload is shown instead, so a broken front door
  // cannot loop the page.
  const reloadKey = "graft.reloadedAt";
  function reauthenticate() {
    const last = Number(sessionStorage.getItem(reloadKey) || 0);
    if (Date.now() - last < 60_000) return false;
    sessionStorage.setItem(reloadKey, String(Date.now()));
    location.reload();
    return true;
  }

  async function api(path, init = {}) {
    // A form sets its own type, with the boundary between its parts.
    const headers = init.body instanceof FormData ? { ...(init.headers || {}) } : { "Content-Type": "application/json", ...(init.headers || {}) };
    const res = await fetch(path, { ...init, headers, credentials: "same-origin" });
    if (res.status === 401 && reauthenticate()) {
      return new Promise(() => {});
    }
    if (!res.ok) {
      let msg = `${res.status}`;
      try { msg = (await res.json()).error || msg; } catch { /* not json */ }
      throw new Error(msg);
    }
    return res.json();
  }

  const stateLabel = {
    received: "received", screening: "sorting", checking: "checking for an outage",
    investigating: "investigating", planning: "planning", building: "building",
    landing: "landing", watching: "watching the pipeline", holding: "waiting on an incident",
    awaiting_plan: "plan waits for a decision", awaiting_review: "waiting for review", stalled: "stopped; waits for a retry",
    answered: "answered", filed: "filed", waiting: "needs a person", released: "released",
    declined: "declined", quarantined: "held a secret", failed: "failed",
  };

  function el(tag, cls, content) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (content !== undefined) e.textContent = content;
    return e;
  }

  function link(label, href) {
    const a = el("a", "", label);
    a.href = href;
    a.target = "_blank";
    a.rel = "noopener";
    return a;
  }

  function fillLanes() {
    const r = me.repositories.find((x) => x.path === repo.value);
    lane.replaceChildren(...(r ? r.lanes : []).map((l) => {
      const o = el("option", "", `${l} lane`);
      o.value = l;
      return o;
    }));
    if (r) lane.value = r.lane;
    lane.hidden = !r || r.lanes.length < 2;
  }

  function renderRepos() {
    const staffed = me.repositories.filter((r) => r.staff);
    repos.replaceChildren(...staffed.map((r) => {
      const row = repoRow.content.firstElementChild.cloneNode(true);
      row.querySelector(".path").textContent = r.path;
      row.querySelector(".stopped").textContent = r.stopped ? `stopped: ${r.reason}` : "shipping";
      const b = row.querySelector(".switch");
      b.textContent = r.stopped ? "Start" : "Stop";
      b.classList.toggle("stop", !r.stopped);
      b.addEventListener("click", async () => {
        b.disabled = true;
        try {
          const reason = r.stopped ? "" : (prompt(`Why stop Graft shipping to ${r.path}?`) || "");
          await api(`/api/repositories/${r.stopped ? "start" : "stop"}`, { method: "POST", body: JSON.stringify({ repository: r.path, reason }) });
          await load();
        } catch (e) { note.textContent = e.message; b.disabled = false; }
      });
      return row;
    }));
  }

  function bullets(items, line) {
    const ul = el("ul");
    for (const it of items || []) ul.append(el("li", "", line(it)));
    return ul;
  }

  const checked = { true: "True", false: "Not so", partly: "Partly", unknown: "Could not check" };
  const decided = { approve: "Approve", approve_with_changes: "Approve with changes", decline: "Decline" };

  // renderBrief is a plan's brief, as its approvers read it: what changed
  // since the round before, then each of its parts. It leads the detail.
  function renderBrief(brief, round, thread) {
    const parts = [el("h3", "", `The plan, round ${round}`)];
    const answered = (thread || []).filter((n) => n.answered === round && !n.replied);
    if (round > 1 && (answered.length || (brief.changed || []).length)) {
      parts.push(el("h4", "", `What changed since round ${round - 1}`));
      for (const n of answered) {
        const q = el("blockquote", "", n.text);
        q.append(el("cite", "", ` ${n.by}, ${n.from === "hub" ? "sending the plan back" : "on the ticket"}`));
        parts.push(q);
      }
      parts.push(bullets(brief.changed, (c) => c));
    }
    parts.push(el("h4", "", "What was asked"), el("p", "", brief.asked));
    parts.push(el("h4", "", "What we will do"), el("p", "", brief.will_do));
    parts.push(el("h4", "", "What we will not do, and why"));
    parts.push((brief.will_not || []).length ? bullets(brief.will_not, (w) => `${w.what} ${w.why}`) : el("p", "note", "Nothing that was asked for is left out."));
    parts.push(el("h4", "", "Judgment calls for you"));
    parts.push((brief.judgment_calls || []).length ? bullets(brief.judgment_calls, (c) => `${c.question} Recommended: ${c.recommendation}`) : el("p", "note", "None."));
    parts.push(el("h4", "", "What we checked"));
    parts.push((brief.checked || []).length ? bullets(brief.checked, (c) => `${c.claim} ${checked[c.found] || c.found}. ${c.detail}${(c.evidence || []).length ? ` (evidence ${c.evidence.join(", ")})` : ""}`) : el("p", "note", "The request states nothing that needed checking."));
    parts.push(el("h4", "", "Fit and risk"), el("p", "", brief.fit), el("p", "", brief.risk));
    const rec = brief.recommendation || {};
    parts.push(el("h4", "", "Recommendation"), el("p", "", `${decided[rec.decision] || rec.decision}. ${rec.why}`));
    return parts;
  }

  // renderBuild is a plan's build plan, folded under its brief.
  function renderBuild(build) {
    const fold = el("details");
    fold.append(el("summary", "", `The build plan (${build.kind})`), el("p", "", build.understood));
    const steps = el("ol");
    for (const s of build.steps || []) steps.append(el("li", "", s));
    fold.append(steps, el("p", "note", "Done when:"), bullets(build.acceptance, (a) => a), el("p", "note", `Tests that fail first: ${build.tests}`));
    if ((build.constraints || []).length) fold.append(el("p", "note", "Constraints:"), bullets(build.constraints, (c) => c));
    fold.append(el("p", "note", "Files it changes:"), bullets(build.changes, (c) => `${c.path}: ${c.what}`));
    return fold;
  }

  // runName names one run of a request, as its comment on the ticket does.
  function runName(run) {
    const names = {
      investigate: `Round ${run.round} of the plan: the investigation`,
      revise: `Round ${run.round} of the plan: a revision`,
      build: run.round ? `The build of round ${run.round}` : "The build, before anyone is asked",
      land: "Landing",
      release: "The release",
    };
    const name = names[run.kind] || run.kind;
    return run.attempt > 1 ? `${name}, attempt ${run.attempt}` : name;
  }

  function renderDetail(box, full) {
    const d = full.detail;
    if (!d) {
      box.replaceChildren(el("p", "note", "Nothing more to show."));
      return;
    }
    const parts = [];
    if (d.plan && d.plan.brief && d.plan.brief.asked) parts.push(...renderBrief(d.plan.brief, d.planRound || 1, d.thread));
    if (d.plan) parts.push(renderBuild(d.plan.build || {}));
    const notes = (d.thread || []);
    if (notes.length) {
      const fold = el("details");
      fold.append(el("summary", "", `What the approvers said (${notes.length})`));
      fold.append(bullets(notes, (n) => `Round ${n.round}, ${n.by}, ${n.from === "hub" ? "sending the plan back" : "on the ticket"}: ${n.text}${n.replied ? " (answered on the ticket)" : n.answered ? ` (answered by round ${n.answered})` : ""}`));
      parts.push(fold);
    }
    for (const was of (d.earlier || []).slice().reverse()) {
      const fold = el("details");
      fold.append(el("summary", "", `Round ${was.n}, as it was`));
      if (was.plan.brief && was.plan.brief.asked) fold.append(...renderBrief(was.plan.brief, was.n, d.thread));
      fold.append(renderBuild(was.plan.build || {}));
      parts.push(fold);
    }
    if (d.verdict) {
      parts.push(el("h3", "", `Verdict: ${d.verdict.verdict}`), el("p", "", d.verdict.summary));
      const facts = el("ul");
      for (const f of d.verdict.facts || []) facts.append(el("li", "", `${f.text} (evidence ${f.evidence.join(", ")})`));
      parts.push(facts);
    }
    if ((d.runs || []).length) {
      parts.push(el("h4", "", "Runs, on the ticket"));
      const how = { done: "done", failed: "stopped" };
      const ul = el("ul");
      for (const run of d.runs) {
        const li = el("li");
        const text = `${runName(run)}: ${how[run.outcome] || "running"}`;
        li.append(run.url ? link(text, run.url) : document.createTextNode(text));
        ul.append(li);
      }
      parts.push(ul);
    }
    if (full.spend && full.spend.runs) parts.push(...renderSpend(full.spend));
    if (full.retry) parts.push(renderRetry(box, full));
    if (d.smallWhy) parts.push(el("p", "note", `Not a small fix: ${d.smallWhy}.`));
    if (d.held) parts.push(el("p", "note", `Holding: ${d.held}.`));
    if (d.error) parts.push(el("p", "error", d.error));
    const links = el("div", "links");
    if (d.runUrl) links.append(link(full.state === "awaiting_plan" ? "decide the plan in the hub" : "the plan's run", d.runUrl));
    if (d.pr) links.append(link("pull request", d.pr.url));
    links.append(link("evidence", `/api/requests/${full.id}/evidence`), link("steps", `/api/requests/${full.id}/events`), link("log", `/api/requests/${full.id}/log`));
    parts.push(links);
    box.replaceChildren(...parts);
  }

  const stageName = { screen: "The screen", investigate: "The investigation", revise: "A revision", build: "The build" };
  const usd = (n) => `$${n.toFixed(2)}`;
  const thousands = (n) => (n || 0).toLocaleString();

  // spendPart names one stage of one round, as the breakdown lists it.
  function spendPart(p) {
    const name = stageName[p.stage] || p.stage;
    return p.round ? `${name}, round ${p.round}` : name;
  }

  // renderSpend is what the request's agents spent, as the agent runtime
  // reported it: the total, each stage and round, and every run, folded.
  // Only whole readers get it from the API.
  function renderSpend(sp) {
    const parts = [el("h4", "", `Spend: ${usd(sp.totalUsd)} in ${sp.runs} agent run${sp.runs === 1 ? "" : "s"}`)];
    const notes = [];
    if (sp.unknown) notes.push(`${sp.unknown} run${sp.unknown === 1 ? "" : "s"} did not report what ${sp.unknown === 1 ? "it" : "they"} spent, and count${sp.unknown === 1 ? "s" : ""} as nothing`);
    if (notes.length) parts.push(el("p", "note", `${notes.join("; ")}.`));
    const table = el("table", "spend");
    const head = el("tr");
    for (const h of ["Stage", "Spent", "Runs", "Turns", "Tokens in", "Tokens out", "Cache read"]) head.append(el("th", "", h));
    table.append(head);
    for (const p of sp.parts || []) {
      const row = el("tr");
      for (const c of [spendPart(p), usd(p.usd), String(p.runs), String(p.turns), thousands(p.inputTokens), thousands(p.outputTokens), thousands(p.cacheReadTokens)]) row.append(el("td", "", c));
      table.append(row);
    }
    parts.push(table);
    const fold = el("details");
    fold.append(el("summary", "", "Every agent run"));
    fold.append(bullets(sp.agentRuns, (a) => {
      const spent = a.cost === "unknown" ? "spend not reported" : `${usd(a.costUsd)}${a.cost === "stopped" ? " (from the stop at its budget)" : ""}`;
      const what = `${spendPart(a)}${a.try ? `, try ${a.try}` : ""}${a.windDown ? ", writing what it had" : ""}`;
      return `${what}: ${a.model || "model not named"}, ${spent}, ${a.turns || 0} turns, ${Math.round((a.durationMs || 0) / 1000)}s, ${a.outcome}`;
    }));
    parts.push(fold);
    return parts;
  }

  // renderRetry says which stage failed and why, and offers its approvers
  // the retry, which runs that stage again on the same request.
  function renderRetry(box, full) {
    const s = full.retry;
    const what = full.state === "awaiting_plan" ? `Round ${s.round + 1} of the plan could not be written` : `Stopped while ${s.stage}`;
    const p = el("p", "error", `${what}: ${s.cause}.`);
    if (full.mayRetry) {
      const b = el("button", "retry", "Retry");
      b.type = "button";
      b.addEventListener("click", async () => {
        b.disabled = true;
        try { renderDetail(box, await api(`/api/requests/${full.id}/retry`, { method: "POST" })); }
        catch (e) { note.textContent = e.message; b.disabled = false; }
      });
      p.append(" ", b);
    }
    return p;
  }

  // planWords says where a request's plan stands: waiting for a decision,
  // or how it was decided, by whom, and when.
  function planWords(r) {
    const p = r.plan;
    if (!p) return "";
    if (r.state === "awaiting_plan" && !p.decision) return `Plan ${p.round} waits for a decision.`;
    if (!p.decision) return "";
    const verb = { approved: "approved", declined: "declined", returned: "sent back" }[p.decision] || p.decision;
    const when = p.decidedAt ? ` on ${new Date(p.decidedAt).toLocaleString()}` : "";
    return `Plan ${p.round} ${verb} by ${p.decidedBy}${when}${p.note ? `: ${p.note}` : "."}`;
  }

  function render(requests) {
    const whole = new Set(me.reads || []);
    list.replaceChildren(...requests.map((r) => {
      const c = card.content.firstElementChild.cloneNode(true);
      c.id = r.id;
      c.querySelector(".title").textContent = r.title || r.text.split("\n")[0].slice(0, 100);
      const state = c.querySelector(".state");
      state.textContent = stateLabel[r.state] || r.state;
      state.className = `state ${r.state}`;
      c.querySelector(".meta").textContent = `${r.repo} · ${r.lane} lane · ${new Date(r.createdAt).toLocaleString()}`;
      c.querySelector(".text").textContent = r.text;
      c.querySelector(".answer").textContent = r.answer || "";
      const plan = c.querySelector(".plan");
      const said = planWords(r);
      if (said) {
        plan.hidden = false;
        plan.replaceChildren(document.createTextNode(said));
        if (r.plan.url) plan.append(" ", link(r.state === "awaiting_plan" ? "Read and decide it in the hub" : "The plan in the hub", r.plan.url));
      }
      const kept = c.querySelector(".files");
      kept.hidden = !(r.attachments || []).length;
      kept.replaceChildren(...(r.attachments || []).map((f) => {
        const li = el("li");
        li.append(link(f.name, `/api/requests/${r.id}/attachments/${f.id}`));
        if (f.from === "approver") li.append(` (from ${f.by}, round ${f.round})`);
        return li;
      }));
      const ticket = c.querySelector(".ticket");
      if (r.ticket) ticket.href = r.ticket.url; else ticket.hidden = true;
      const incident = c.querySelector(".incident");
      if (r.incident && r.incident.url) incident.href = r.incident.url; else incident.hidden = true;
      const more = c.querySelector(".more");
      const box = c.querySelector(".detail");
      more.hidden = !whole.has(r.repo);
      const show = async () => {
        box.hidden = false;
        try { renderDetail(box, await api(`/api/requests/${r.id}`)); }
        catch (e) { box.replaceChildren(el("p", "error", e.message)); }
      };
      more.addEventListener("click", () => {
        if (open.has(r.id)) { open.delete(r.id); box.hidden = true; } else { open.add(r.id); show(); }
      });
      if (open.has(r.id)) show();
      return c;
    }));
  }

  async function refresh() {
    try {
      const requests = await api("/api/requests");
      if (wanted && !requests.some((r) => r.id === wanted)) {
        try { requests.unshift(await api(`/api/requests/${encodeURIComponent(wanted)}`)); }
        catch (e) { note.textContent = `Request ${wanted}: ${e.message}`; wanted = ""; }
      }
      if (wanted && !arrived && (me.reads || []).some((p) => requests.find((r) => r.id === wanted)?.repo === p)) open.add(wanted);
      render(requests);
      const card = wanted && !arrived ? document.getElementById(wanted) : null;
      if (card) {
        arrived = true;
        card.scrollIntoView({ block: "start" });
      }
    } catch (e) { note.textContent = e.message; }
  }

  async function load() {
    me = await api("/api/me");
    who.textContent = `${me.email} · ${me.program}`;
    const kept = repo.value;
    repo.replaceChildren(...me.repositories.map((r) => {
      const o = el("option", "", r.path);
      o.value = r.path;
      return o;
    }));
    if (me.repositories.some((r) => r.path === kept)) repo.value = kept;
    repo.hidden = me.repositories.length < 2;
    form.hidden = me.repositories.length === 0;
    document.getElementById("no-lanes").hidden = me.repositories.length !== 0;
    fillLanes();
    renderRepos();
    await refresh();
  }

  repo.addEventListener("change", fillLanes);

  form.addEventListener("submit", async (ev) => {
    ev.preventDefault();
    go.disabled = true;
    note.textContent = "";
    try {
      const request = JSON.stringify({ repository: repo.value, lane: lane.value, text: text.value });
      let body = request;
      if (files.files.length) {
        body = new FormData();
        body.append("request", new Blob([request], { type: "application/json" }));
        for (const f of files.files) body.append("attachments", f, f.name);
      }
      await api("/api/requests", { method: "POST", body });
      text.value = "";
      files.value = "";
      await refresh();
    } catch (e) {
      note.textContent = e.message;
    } finally {
      go.disabled = false;
    }
  });

  load().then(() => setInterval(refresh, 5000)).catch((e) => { note.textContent = e.message; form.hidden = false; });
})();
