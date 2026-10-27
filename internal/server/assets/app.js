'use strict';

const POLL_MS = 1500;
const CONTEXT = 3; // unchanged lines kept around each change when folding

// App shortcuts use the platform's command key: ⌘ on macOS, Ctrl elsewhere.
// On macOS Ctrl is left to the text system (Ctrl-F moves the caret) and to
// the Vim keys.
const MAC = /^Mac/.test(navigator.platform);
const commandKey = event => MAC ? event.metaKey && !event.ctrlKey : event.ctrlKey && !event.metaKey;
const commandLabel = key => (MAC ? '⌘' : 'Ctrl+') + key;

const state = {
  session: null,
  files: [],          // [{path, oldPath, status, hash}]
  filesKey: '',
  reviewed: {},       // path -> hash
  comments: [],
  overall: '',
  selected: null,     // path shown in #content
  view: null,         // server View for `selected`
  renderedKey: '',    // what #content currently shows
  layout: 'split',
  selection: null,    // {start, end} zero-based new-side lines
  cursor: null,       // zero-based new-side line of the current line
  cursorFold: null,   // data-key of the fold the cursor is on instead of a line
  visual: null,       // anchor line while in visual line mode
  hiddenThreads: new Set(), // root ids folded away with zc / zm
  openResolved: new Set(),  // resolved root ids expanded with zo or a click
  picker: null,       // {ids, index} while choosing a thread or comment by number
  drag: null,         // {anchor} while selecting with the mouse
  composer: null,     // {kind: 'new'|'reply'|'edit', line, endLine, rootId, commentId}
  pendingTarget: null,
  expanded: new Set(), // `${path}\0${itemKey}` of unfolded runs
  dirOpen: new Map(),
  threadFilter: 'waiting',
};

const $ = selector => document.querySelector(selector);

function el(tag, attrs = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs)) {
    if (value === undefined || value === null || value === false) continue;
    if (key.startsWith('on')) node.addEventListener(key.slice(2), value);
    else if (key === 'class') node.className = value;
    else if (key === 'style') node.style.cssText = value;
    else node.setAttribute(key, value === true ? '' : value);
  }
  node.append(...children.flat().filter(child => child !== null && child !== undefined && child !== false));
  return node;
}

// ---- Errors -----------------------------------------------------------------
// Every failure ends up here, through the global handlers: code below never
// catches. The banner stays until the user dismisses it or a newer failure
// replaces it.

function showError(error) {
  const text = error instanceof Error ? error.message : String(error);
  $('#error-text').textContent = text;
  $('#error').hidden = false;
}
window.addEventListener('error', event => showError(event.error ?? event.message));
window.addEventListener('unhandledrejection', event => showError(event.reason));
$('#error-close').addEventListener('click', () => { $('#error').hidden = true; });

async function api(method, url, body) {
  const init = { method, headers: {} };
  if (body !== undefined) {
    init.headers['Content-Type'] = 'application/json';
    init.body = JSON.stringify(body);
  }
  const response = await fetch(url, init);
  const data = await response.json();
  if (!response.ok) throw new Error(`${method} ${url} failed (${response.status}): ${data.error}`);
  return data;
}

function toast(text) {
  const node = $('#toast');
  node.textContent = text;
  node.hidden = false;
  clearTimeout(toast.timer);
  toast.timer = setTimeout(() => { node.hidden = true; }, 1800);
}

// Shows a modal and resolves to the value of the chosen button, or null.
// The keyboard drives it without relying on button focus (see dialogKey):
// Enter picks the highlighted choice, y the last (confirming) one, Esc / n
// cancel, arrows and Tab move the highlight. It starts on the last choice.
let openDialog = null;

function dialog(text, choices) {
  $('#modal-text').textContent = text;
  return new Promise(resolve => {
    const options = [{ label: 'Cancel', value: null }, ...choices];
    const done = value => {
      openDialog = null;
      $('#modal').hidden = true;
      resolve(value);
    };
    const buttons = options.map(option => el('button', { class: option.class, onclick: () => done(option.value) }, option.label));
    $('#modal .actions').replaceChildren(...buttons);
    openDialog = { options, buttons, index: options.length - 1, done };
    paintDialog();
    $('#modal').hidden = false;
  });
}

function paintDialog() {
  openDialog.buttons.forEach((button, i) => button.classList.toggle('focused', i === openDialog.index));
}

function dialogKey(event) {
  const d = openDialog;
  event.preventDefault();
  if (event.key === 'Enter') d.done(d.options[d.index].value);
  else if (event.key === 'y') d.done(d.options.at(-1).value);
  else if (event.key === 'Escape' || event.key === 'n') d.done(null);
  else if (['ArrowLeft', 'ArrowRight', 'Tab', 'h', 'l'].includes(event.key)) {
    const back = event.key === 'ArrowLeft' || event.key === 'h' || (event.key === 'Tab' && event.shiftKey);
    d.index = (d.index + (back ? -1 : 1) + d.options.length) % d.options.length;
    paintDialog();
  }
}

async function copyText(text, message) {
  await navigator.clipboard.writeText(text);
  toast(message);
}

// ---- Helpers ----------------------------------------------------------------

const roots = () => state.comments.filter(c => !c.parentId);
const threadOf = rootId => state.comments.filter(c => c.id === rootId || c.parentId === rootId);
const threadState = rootId => state.comments.find(c => c.id === rootId).resolved ? 'resolved'
  : threadOf(rootId).at(-1).source === 'agent' ? 'answered' : 'waiting';
const STATE_LABEL = { waiting: 'Waiting for AI', answered: 'AI replied', resolved: 'Resolved' };
const inFilter = root => state.threadFilter === 'all' || threadState(root.id) === state.threadFilter;
const lineLabel = (line, endLine) => line === endLine ? `L${line + 1}` : `L${line + 1}–${endLine + 1}`;
const isReviewed = file => !!file?.hash && state.reviewed[file.path] === file.hash;
const fileEntry = path => state.files.find(f => f.path === path);
const typing = () => ['TEXTAREA', 'INPUT'].includes(document.activeElement?.tagName);
const basename = path => path.slice(path.lastIndexOf('/') + 1);

function formatTime(iso) {
  const date = new Date(iso);
  const sameDay = date.toDateString() === new Date().toDateString();
  return sameDay
    ? date.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })
    : date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
}

// ---- Loading ----------------------------------------------------------------

async function loadSession() {
  state.session = await api('GET', '/ui/session');
  state.layout = state.session.layout;
  const { root, diff, diffArgs } = state.session;
  document.title = diff ? `${basename(root)} — git diff ${diffArgs.join(' ')}` : basename(root);
  $('#progress-pane').hidden = !diff;
}

async function refreshFiles() {
  const data = await api('GET', '/ui/files');
  const key = JSON.stringify(data);
  if (key === state.filesKey) return;
  state.filesKey = key;
  state.files = data.files;
  state.reviewed = data.reviewed;
  renderTree();
  renderHeader();
}

async function refreshComments() {
  const data = await api('GET', '/v1/comments');
  state.comments = data.comments;
  state.overall = data.overall;
  if (document.activeElement !== $('#overall')) {
    $('#overall').value = data.overall;
    overallSent = data.overall;
  }
  renderThreads();
  renderTree();
  renderView();
}

async function refreshView() {
  if (!state.selected) return;
  const path = state.selected;
  const view = await api('GET', `/ui/view?path=${encodeURIComponent(path)}`);
  if (state.selected !== path) return; // another file was opened meanwhile
  state.view = view;
  renderView();
  renderHeader();
}

function pollLoop() {
  Promise.all([refreshFiles(), refreshView()])
    .finally(() => setTimeout(pollLoop, POLL_MS));
}

async function openFile(path, target) {
  const switching = state.selected !== path;
  if (switching) {
    state.selected = path;
    state.view = null;
    state.selection = null;
    state.cursor = null;
    state.cursorFold = null;
    state.visual = null;
    state.composer = null;
    for (let i = path.indexOf('/'); i >= 0; i = path.indexOf('/', i + 1)) state.dirOpen.set(path.slice(0, i), true);
    renderTree();
    renderThreads();
    renderHeader();
    renderView();
  }
  state.pendingTarget = target ?? null;
  await refreshView();
  if (state.pendingTarget) scrollToPendingTarget();
  else if (switching) jumpToChange(1, true);
}

// ---- File tree --------------------------------------------------------------

function buildTree(files) {
  const root = { dirs: new Map(), files: [] };
  for (const file of files) {
    const parts = file.path.split('/');
    let node = root;
    for (const part of parts.slice(0, -1)) {
      if (!node.dirs.has(part)) node.dirs.set(part, { dirs: new Map(), files: [] });
      node = node.dirs.get(part);
    }
    node.files.push({ name: parts.at(-1), file });
  }
  return root;
}


// The tree's files in display order; also drives next/previous file.
function orderedFiles() {
  const out = [];
  const walk = node => {
    for (const dir of [...node.dirs.keys()].sort()) walk(node.dirs.get(dir));
    for (const entry of node.files) out.push(entry.file);
  };
  walk(buildTree(state.files));
  return out;
}

// Every tree row is [indent][twisty][checkbox (diff only)][name][extras], so
// names line up by depth alone; the indent span alone draws the guides.
function indent(depth) {
  return el('span', { class: 'indent', style: `width:${depth * 16}px` });
}

// A reviewed checkbox for a file, or for a directory covering all its files:
// checked when all are reviewed, indeterminate when some are.
function reviewBox(files) {
  const done = files.filter(isReviewed).length;
  const box = el('input', {
    type: 'checkbox', checked: done === files.length,
    title: files.length === 1 ? 'Reviewed' : `${done} / ${files.length} reviewed`,
    onclick: event => event.stopPropagation(),
    onchange: event => setReviewedMany(files, event.target.checked),
  });
  box.indeterminate = done > 0 && done < files.length;
  return box;
}

function allFiles(node) {
  return [...node.files.map(entry => entry.file), ...[...node.dirs.values()].flatMap(allFiles)];
}

function renderTree() {
  const container = $('#tree');
  const scrollTop = container.scrollTop;
  const diff = state.session.diff;
  const threadsByPath = new Map();
  for (const c of roots()) {
    if (c.resolved) continue; // done; nothing left to look at
    const list = threadsByPath.get(c.path) ?? [];
    list.push(threadState(c.id));
    threadsByPath.set(c.path, list);
  }
  const forceOpen = diff;
  const rows = [];

  const walk = (node, prefix, depth) => {
    for (const name of [...node.dirs.keys()].sort()) {
      // Compact chains of single-child directories.
      let child = node.dirs.get(name);
      let label = name;
      while (child.files.length === 0 && child.dirs.size === 1) {
        const [only] = child.dirs.keys();
        label += '/' + only;
        child = child.dirs.get(only);
      }
      const path = prefix + label;
      const open = state.dirOpen.get(path) ?? forceOpen;
      rows.push(el('div', {
        class: 'node dir',
        onclick: () => { state.dirOpen.set(path, !open); renderTree(); },
      },
      indent(depth),
      el('span', { class: `twisty${open ? ' open' : ''}` }, icon('chevron')),
      diff && reviewBox(allFiles(child)),
      el('span', { class: 'name' }, label)));
      if (open) walk(child, path + '/', depth + 1);
    }
    for (const { name, file } of node.files) {
      const threads = threadsByPath.get(file.path) ?? [];
      const waiting = threads.filter(s => s === 'waiting').length;
      const reviewed = isReviewed(file);
      rows.push(el('div', {
        class: `node file${file.path === state.selected ? ' selected' : ''}${reviewed ? ' reviewed' : ''}`,
        title: file.oldPath ? `${file.oldPath} → ${file.path}` : file.path,
        onclick: () => openFile(file.path),
      },
      indent(depth),
      el('span', { class: 'twisty file-icon' }, icon('file', 14)),
      diff && reviewBox([file]),
      el('span', { class: 'name' }, name),
      threads.length > 0 && el('span', {
        class: `pip ${waiting ? 'waiting' : 'answered'}`,
        title: `${threads.length} thread(s), ${waiting} waiting for AI`,
      }, String(threads.length)),
      diff && el('span', { class: `status status-${file.status}` }, file.status)));
    }
  };
  walk(buildTree(state.files), '', 0);
  if (rows.length === 0) {
    rows.push(diff
        ? el('div', { class: 'empty' }, el('strong', {}, 'No changes'), 'This diff is empty.')
        : el('div', { class: 'empty' }, 'No files.'));
  }
  container.replaceChildren(...rows);
  container.scrollTop = scrollTop;

  if (diff) {
    const done = state.files.filter(isReviewed).length;
    const total = state.files.length;
    $('#progress-fill').style.width = total ? `${(done / total) * 100}%` : '0';
    $('#progress-text').textContent = done === total && total > 0 ? 'All reviewed' : `${done} / ${total} reviewed`;
    $('#progress').classList.toggle('done', done === total && total > 0);
  }
}

async function setReviewed(file, checked) {
  await setReviewedMany([file], checked);
}

async function setReviewedMany(files, checked) {
  for (const file of files) {
    await api('PUT', '/ui/reviewed', { path: file.path, hash: checked ? file.hash : '' });
    if (checked) state.reviewed[file.path] = file.hash;
    else delete state.reviewed[file.path];
  }
  renderTree();
  renderHeader();
}

// ---- Find in file -----------------------------------------------------------
// Matches are painted with the CSS Custom Highlight API, so they can span the
// syntax-highlighting spans without touching the DOM. Folded lines that match
// are kept visible by keptItems.

const search = { query: '', ranges: [], current: 0, stale: true };

function lineMatchesSearch(line) {
  return search.query !== '' && line.t.toLowerCase().includes(search.query.toLowerCase());
}

function openSearch() {
  $('#search').hidden = false;
  const input = $('#search-input');
  input.focus();
  input.select();
}

function closeSearch() {
  $('#search').hidden = true;
  search.query = '';
  renderView();
  updateSearch();
}

// Recomputes the match ranges from the rendered cells.
function updateSearch() {
  CSS.highlights.delete('search');
  CSS.highlights.delete('search-current');
  search.ranges = [];
  const needle = search.query.toLowerCase();
  if (needle) {
    for (const cell of document.querySelectorAll('#content .row .text')) {
      const nodes = [];
      const walker = document.createTreeWalker(cell, NodeFilter.SHOW_TEXT);
      let text = '';
      for (let node = walker.nextNode(); node; node = walker.nextNode()) {
        nodes.push({ node, start: text.length });
        text += node.data;
      }
      const lower = text.toLowerCase();
      const locate = offset => {
        let i = nodes.length - 1;
        while (i > 0 && nodes[i].start > offset) i--;
        return [nodes[i].node, offset - nodes[i].start];
      };
      for (let at = lower.indexOf(needle); at >= 0; at = lower.indexOf(needle, at + needle.length)) {
        const range = new Range();
        range.setStart(...locate(at));
        range.setEnd(...locate(at + needle.length - 1));
        range.setEnd(range.endContainer, range.endOffset + 1);
        search.ranges.push(range);
      }
    }
  }
  if (search.stale) search.current = Math.min(search.current, Math.max(0, search.ranges.length - 1));
  search.stale = false;
  paintSearch();
}

function paintSearch() {
  const count = $('#search-count');
  count.classList.toggle('none', search.query !== '' && search.ranges.length === 0);
  count.textContent = !search.query ? '' : search.ranges.length ? `${search.current + 1} / ${search.ranges.length}` : 'No results';
  if (!search.ranges.length) return;
  CSS.highlights.set('search', new Highlight(...search.ranges));
  CSS.highlights.set('search-current', new Highlight(search.ranges[search.current]));
}

function revealCurrentMatch() {
  const range = search.ranges[search.current];
  if (!range) return;
  const content = $('#content');
  const box = range.getBoundingClientRect();
  const view = content.getBoundingClientRect();
  if (box.top < view.top + 40 || box.bottom > view.bottom - 20) {
    content.scrollTop += box.top - view.top - content.clientHeight / 3;
  }
}

function stepSearch(delta) {
  if (!search.ranges.length) { if (search.query) toast(`No matches for "${search.query}"`); return; }
  search.current = ((search.current + delta) % search.ranges.length + search.ranges.length) % search.ranges.length;
  paintSearch();
  revealCurrentMatch();
  const row = search.ranges[search.current].startContainer.parentElement.closest('.row[data-line]');
  if (row) setCursor(Number(row.dataset.line), false);
}

$('#search-input').addEventListener('input', event => {
  search.query = event.target.value;
  search.current = 0;
  renderView();
  updateSearch();
  // Start from the first match at or after the current line, like Vim.
  const top = (rowOfLine(state.cursor) ?? $('#content')).getBoundingClientRect().top;
  const below = search.ranges.findIndex(range => range.getBoundingClientRect().top >= top);
  search.current = Math.max(0, below);
  paintSearch();
  revealCurrentMatch();
});
$('#search-input').addEventListener('keydown', event => {
  if (event.isComposing) return;
  // Enter accepts the query and returns to normal mode, where n / p step.
  if (event.key === 'Enter') { event.preventDefault(); event.target.blur(); }
  if (event.key === 'Escape') { event.preventDefault(); closeSearch(); }
});
$('#search-prev').addEventListener('click', () => stepSearch(-1));
$('#search-next').addEventListener('click', () => stepSearch(1));
$('#search-close').addEventListener('click', closeSearch);

// ---- Fuzzy finder -----------------------------------------------------------

const finder = { hits: [], active: 0 };

// Scores `path` against `query` as a subsequence, preferring consecutive
// characters, characters right after a separator, and the file name. Every
// occurrence of the first character is tried as a starting point and the best
// match wins. Returns null when the query is not a subsequence.
function fuzzyMatch(query, path) {
  const needle = query.toLowerCase().replaceAll(' ', '');
  const lower = path.toLowerCase();
  const nameStart = path.lastIndexOf('/') + 1;
  let best = null;
  for (let start = lower.indexOf(needle[0]); start >= 0; start = lower.indexOf(needle[0], start + 1)) {
    const positions = [start];
    let from = start + 1;
    for (const ch of needle.slice(1)) {
      const i = lower.indexOf(ch, from);
      if (i < 0) break;
      positions.push(i);
      from = i + 1;
    }
    if (positions.length < needle.length) break; // later starts cannot do better
    let score = 0;
    positions.forEach((i, k) => {
      score += 1;
      if (k > 0 && i === positions[k - 1] + 1) score += 6;
      if (k > 0) score -= Math.min(i - positions[k - 1] - 1, 6);
      if (i === 0 || '/._-'.includes(path[i - 1])) score += 5;
      if (i >= nameStart) score += 3;
    });
    score -= path.length * 0.02;
    if (!best || score > best.score) best = { score, positions };
  }
  return best;
}

function openFinder() {
  $('#finder').hidden = false;
  $('#finder-input').value = '';
  renderFinder();
  $('#finder-input').focus();
}

function closeFinder() {
  $('#finder').hidden = true;
}

function renderFinder() {
  const query = $('#finder-input').value.trim();
  const files = query ? state.files : orderedFiles();
  finder.hits = files
    .map(file => ({ file, match: query ? fuzzyMatch(query, file.path) : { score: 0, positions: [] } }))
    .filter(hit => hit.match)
    .sort((a, b) => b.match.score - a.match.score)
    .slice(0, 200);
  finder.active = Math.min(finder.active, Math.max(0, finder.hits.length - 1));
  if (!query) finder.active = Math.max(0, finder.hits.findIndex(hit => hit.file.path === state.selected));
  const marked = (text, offset, positions) => {
    const parts = [];
    for (let i = 0; i < text.length; i++) {
      parts.push(positions.has(offset + i) ? el('mark', {}, text[i]) : text[i]);
    }
    return parts;
  };
  $('#finder-list').replaceChildren(...finder.hits.map((hit, index) => {
    const path = hit.file.path;
    const slash = path.lastIndexOf('/');
    const positions = new Set(hit.match.positions);
    return el('div', {
      class: `hit${index === finder.active ? ' active' : ''}`,
      onmousemove: () => { if (finder.active !== index) { finder.active = index; renderFinder(); } },
      onclick: () => chooseHit(index),
    },
    state.session.diff && el('span', { class: `status status-${hit.file.status}` }, hit.file.status),
    el('span', { class: 'file' }, marked(path.slice(slash + 1), slash + 1, positions)),
    el('span', { class: 'dir' }, marked(path.slice(0, slash + 1), 0, positions)));
  }), ...(finder.hits.length ? [] : [el('div', { class: 'empty' }, 'No matching files.')]));
  $('#finder-list .hit.active')?.scrollIntoView({ block: 'nearest' });
}

function chooseHit(index) {
  const hit = finder.hits[index];
  if (!hit) return;
  closeFinder();
  openFile(hit.file.path);
}

$('#finder-input').addEventListener('input', () => { finder.active = 0; renderFinder(); });
$('#finder-input').addEventListener('keydown', event => {
  if (event.isComposing) return;
  const move = delta => {
    event.preventDefault();
    finder.active = Math.max(0, Math.min(finder.hits.length - 1, finder.active + delta));
    renderFinder();
  };
  if (event.key === 'ArrowDown' || (event.ctrlKey && event.key === 'n')) move(1);
  else if (event.key === 'ArrowUp' || (event.ctrlKey && event.key === 'p')) move(-1);
  else if (event.key === 'Enter') { event.preventDefault(); chooseHit(finder.active); }
  else if (event.key === 'Escape') { event.preventDefault(); closeFinder(); }
});
$('#finder').addEventListener('mousedown', event => {
  if (event.target === event.currentTarget) closeFinder();
});

// ---- File header ------------------------------------------------------------

const STATUS_NAMES = { A: 'Added', M: 'Modified', D: 'Deleted', R: 'Renamed', C: 'Copied', T: 'Type changed', U: 'Unmerged' };

function renderHeader() {
  const path = state.selected ?? '';
  $('#layout-toggle').hidden = !(state.view?.status);
  for (const toggle of document.querySelectorAll('#layout-toggle button')) {
    toggle.classList.toggle('active', toggle.dataset.layout === state.layout);
  }
  const slash = path.lastIndexOf('/');
  $('#crumb-dir').textContent = path.slice(0, slash + 1);
  $('#crumb-name').textContent = path.slice(slash + 1);
  $('#crumb').title = path;

  const view = state.view;
  let status = view?.status ? STATUS_NAMES[view.status] ?? view.status : '';
  if (view?.oldPath) status += ` from ${view.oldPath}`;
  $('#file-status').textContent = status;

  const stat = $('#file-stat');
  stat.replaceChildren();
  if (view?.status) {
    const adds = view.lines.filter(l => l.k === '+').length;
    const dels = view.lines.filter(l => l.k === '-').length;
    stat.append(el('span', { class: 'a' }, `+${adds}`), el('span', { class: 'd' }, `−${dels}`));
  }
  $('#change-nav').hidden = !view?.lines.some(l => l.k !== ' ');
  if (!path) return;
}

$('#prev-change').addEventListener('click', () => jumpToChange(-1));
$('#next-change').addEventListener('click', () => jumpToChange(1));

for (const button of document.querySelectorAll('#layout-toggle button')) {
  button.addEventListener('click', async () => {
    state.layout = button.dataset.layout;
    renderHeader();
    renderView();
    await api('PUT', '/ui/layout', { layout: state.layout });
  });
}

// ---- Threads panel ----------------------------------------------------------

function renderThreads() {
  const all = roots().sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line);
  const counts = { all: all.length, waiting: 0, answered: 0, resolved: 0 };
  for (const root of all) counts[threadState(root.id)]++;
  for (const button of document.querySelectorAll('#thread-filter button')) {
    button.classList.toggle('active', button.dataset.filter === state.threadFilter);
    button.querySelector('span').textContent = counts[button.dataset.filter] || '';
  }
  const shown = all.filter(inFilter);

  const rows = [];
  let currentPath = null;
  for (const root of shown) {
    if (root.path !== currentPath) {
      currentPath = root.path;
      rows.push(el('div', { class: 'group-head', title: root.path }, root.path));
    }
    const thread = threadOf(root.id);
    const status = threadState(root.id);
    const replies = thread.length - 1;
    rows.push(el('div', {
      class: `thread-item ${status}${root.path === state.selected ? ' current' : ''}`,
      title: `${STATE_LABEL[status]}\n\n${root.body}`,
      onclick: () => openFile(root.path, { line: root.line, endLine: root.endLine, commentId: root.id }),
    },
    el('div', { class: 'bar' }),
    el('div', {},
      el('div', { class: 'title' },
        el('span', { class: 'lines' }, lineLabel(root.line, root.endLine)),
        el('span', { class: 'preview' }, root.body.split('\n')[0])),
      el('div', { class: 'sub' },
        `${root.author}${replies ? ` + ${replies} ${replies === 1 ? 'reply' : 'replies'}` : ''}`)),
    el('div', { class: 'hover-actions' },
      el('button', { title: 'Copy comment ID', onclick: event => { event.stopPropagation(); copyText(root.id, 'Copied comment ID'); } }, icon('copy'), 'ID'),
      el('button', {
        class: 'square', title: root.resolved ? 'Unresolve' : 'Resolve',
        onclick: event => { event.stopPropagation(); setResolved(root.id, !root.resolved); },
      }, icon(root.resolved ? 'reopen' : 'check')),
      el('button', { class: 'square danger-text', title: 'Delete thread', onclick: event => { event.stopPropagation(); removeComment(root.id); } }, icon('trash')))));
  }
  if (rows.length === 0) {
    rows.push(all.length
      ? el('div', { class: 'empty' }, 'No threads in this filter.')
      : el('div', { class: 'empty' }, el('strong', {}, 'No comments yet'), 'Press a on a line, or click its number, to start a thread.'));
  }
  $('#threads').replaceChildren(...rows);
}

for (const button of document.querySelectorAll('#thread-filter button')) {
  button.addEventListener('click', () => {
    state.threadFilter = button.dataset.filter;
    renderThreads();
  });
}

// ---- File view --------------------------------------------------------------

function renderView() {
  const content = $('#content');
  const view = state.view;
  const fileComments = view ? state.comments.filter(c => c.path === view.path) : [];
  const key = JSON.stringify([state.selected, view?.version, view?.path, state.layout, fileComments, state.composer,
    [...state.expanded], state.pendingTarget, search.query, [...state.hiddenThreads], [...state.openResolved], state.picker]);
  if (key === state.renderedKey) {
    applySelection();
    return;
  }
  search.stale = true;
  state.renderedKey = key;

  if (!view) {
    content.replaceChildren(state.selected
      ? el('div', { class: 'empty big' }, 'Loading…')
      : state.files.length === 0
        ? el('div', { class: 'empty big' }, el('strong', {}, state.session.diff ? 'No changes' : 'No files'))
        : el('div', { class: 'empty big' },
          el('strong', {}, 'No file open'),
          el('kbd', {}, '}'), ' next file · ', el('kbd', {}, commandLabel('E')), ' go to file · ', el('kbd', {}, '?'), ' shortcuts'));
    return;
  }

  const anchor = captureAnchor();
  const composerFocus = captureComposerFocus();
  let maxLine = 0;
  let maxNumber = 0;
  for (const line of view.lines) {
    maxLine = Math.max(maxLine, line.n ?? 0);
    maxNumber = Math.max(maxNumber, line.n ?? 0, line.o ?? 0);
  }
  // Files without a change status are plain text even in diff mode.
  const mode = !view.status ? 'plain' : state.layout;
  const body = el('div', { class: `code ${mode}`, style: `--num-w:${Math.max(3, String(maxNumber).length) + 3}ch` });

  const threadRoots = fileComments.filter(c => !c.parentId);
  const shown = root => !state.hiddenThreads.has(root.id);
  const byEnd = new Map();
  const outside = [];
  const marks = new Map(); // zero-based line -> {status, start, end}
  for (const root of threadRoots) {
    if (root.endLine >= maxLine) { outside.push(root); continue; }
    if (!byEnd.has(root.endLine)) byEnd.set(root.endLine, []);
    if (shown(root)) byEnd.get(root.endLine).push(root);
    const status = threadState(root.id);
    for (let line = root.line; line <= root.endLine; line++) {
      const mark = marks.get(line) ?? { status, start: false, end: false };
      if (MARK_RANK[status] > MARK_RANK[mark.status]) mark.status = status; // the louder mark wins
      mark.start ||= line === root.line;
      mark.end ||= line === root.endLine;
      marks.set(line, mark);
    }
  }
  const afterNew = line => {
    const nodes = (byEnd.get(line) ?? []).map(renderThread);
    if (state.composer?.kind === 'new' && state.composer.endLine === line) nodes.push(composerEl);
    return nodes;
  };

  if (view.notice) body.append(el('div', { class: 'notice' }, view.notice));
  const syntax = highlightView(view);
  const code = (cls, line, side) => {
    const cell = el('div', { class: cls });
    if (!line) return cell;
    const html = side === 'old' ? syntax?.old.get(line.o) : syntax?.new.get(line.n);
    if (html === undefined) cell.textContent = line.t;
    else cell.innerHTML = html;
    return cell;
  };

  const items = mode === 'split'
    ? pairLines(view.lines).map(([left, right]) => ({ left, right, n: right?.n, changed: left?.k === '-' || right?.k === '+' }))
    : view.lines.map(line => ({ line, n: line.n, changed: line.k !== ' ' }));
  const visible = foldable(view) ? keptItems(items, marks) : items.map(() => true);

  let previousChanged = false;
  for (let i = 0; i < items.length;) {
    if (!visible[i]) {
      let end = i;
      while (end < items.length && !visible[end]) end++;
      const runKey = `${view.path}\0${itemKey(items[i])}`;
      const count = end - i;
      const fold = el('div', {
        class: 'fold', 'data-key': `f${itemKey(items[i])}`,
        onclick: () => expandFold(fold),
      }, `Show ${count} unchanged line${count === 1 ? '' : 's'}`);
      fold.runKey = runKey;
      body.append(fold);
      i = end;
      previousChanged = false;
      continue;
    }
    const item = items[i];
    const row = mode === 'split' ? splitRow(item.left, item.right, code)
      : mode === 'plain' ? plainRow(item.line, code) : inlineRow(item.line, code);
    if (item.changed && !previousChanged) row.classList.add('change-start');
    previousChanged = item.changed;
    const mark = item.n ? marks.get(item.n - 1) : undefined;
    if (mark) {
      row.dataset.mark = mark.status;
      if (mark.start) row.classList.add('mark-start');
      if (mark.end) row.classList.add('mark-end');
    }
    body.append(row);
    if (item.n) body.append(...afterNew(item.n - 1));
    i++;
  }
  if (outside.length) {
    body.append(el('div', { class: 'outside-head' }, 'Threads outside the current content'), ...outside.filter(shown).map(renderThread));
  }
  content.replaceChildren(body);
  applySelection();
  restoreAnchor(anchor);
  restoreComposerFocus(composerFocus);
  updateSearch();
}

const MARK_RANK = { resolved: 0, answered: 1, waiting: 2 };

function foldable(view) {
  return !!view.status && view.lines.some(l => l.k !== ' ');
}

const itemKey = item => {
  const line = item.line ?? item.right ?? item.left;
  return item.n ? `n${item.n}` : `o${line.o}`;
};

// Decides which rows of a diff stay visible: changes with some context, and
// every line something points at (threads, selection, composer, navigation).
function keptItems(items, marks) {
  const pinned = new Set(marks.keys());
  const pin = (start, end) => { for (let line = start; line <= end; line++) pinned.add(line); };
  if (state.selection) pin(state.selection.start, state.selection.end);
  if (state.composer?.kind === 'new') pin(state.composer.line, state.composer.endLine);
  if (state.pendingTarget) pin(state.pendingTarget.line, state.pendingTarget.endLine);
  const visible = items.map((item, i) => {
    for (let j = Math.max(0, i - CONTEXT); j <= Math.min(items.length - 1, i + CONTEXT); j++) {
      if (items[j].changed) return true;
    }
    if (item.n !== undefined && pinned.has(item.n - 1)) return true;
    return [item.line, item.left, item.right].some(line => line && lineMatchesSearch(line));
  });
  // Unfold runs the user expanded, and never hide a run of just one or two.
  for (let i = 0; i < items.length;) {
    if (visible[i]) { i++; continue; }
    let end = i;
    while (end < items.length && !visible[end]) end++;
    if (end - i <= 2 || state.expanded.has(`${state.view.path}\0${itemKey(items[i])}`)) {
      for (let j = i; j < end; j++) visible[j] = true;
    }
    i = end;
  }
  return visible;
}

// ---- Syntax highlighting -----------------------------------------------------
// Each side of the file is highlighted as a whole, so strings and comments that
// span lines are colored correctly, then split back into lines.

const MAX_HIGHLIGHT_CHARS = 512 * 1024;
const highlightCache = { version: null, value: null };

function highlightView(view) {
  if (highlightCache.version !== view.version) {
    highlightCache.version = view.version;
    highlightCache.value = computeHighlight(view);
  }
  return highlightCache.value;
}

function computeHighlight(view) {
  const ext = view.path.includes('.') ? view.path.slice(view.path.lastIndexOf('.') + 1).toLowerCase() : basename(view.path).toLowerCase();
  const language = hljs.getLanguage(ext) ? ext : null;
  if (!language) return null;
  const highlightSide = lines => {
    if (lines.length === 0) return [];
    const text = lines.map(l => l.t).join('\n');
    if (text.length > MAX_HIGHLIGHT_CHARS) return null;
    const html = splitHighlighted(hljs.highlight(text, { language, ignoreIllegals: true }).value);
    if (html.length !== lines.length) throw new Error(`highlighting ${view.path} produced ${html.length} lines for ${lines.length}`);
    return html;
  };
  const oldLines = view.lines.filter(l => l.o);
  const newLines = view.lines.filter(l => l.n);
  const oldHtml = highlightSide(oldLines);
  const newHtml = highlightSide(newLines);
  if (!oldHtml || !newHtml) return null;
  return {
    old: new Map(oldLines.map((l, i) => [l.o, oldHtml[i]])),
    new: new Map(newLines.map((l, i) => [l.n, newHtml[i]])),
  };
}

// Splits highlight.js output into lines, closing the spans that are open at
// each line break and reopening them on the next line.
function splitHighlighted(html) {
  const lines = [];
  const open = [];
  let current = '';
  for (const [, start, end, newline, text] of html.matchAll(/(<span[^>]*>)|(<\/span>)|(\n)|([^<\n]+)/g)) {
    if (start) { open.push(start); current += start; }
    else if (end) { open.pop(); current += end; }
    else if (newline) { lines.push(current + '</span>'.repeat(open.length)); current = open.join(''); }
    else current += text;
  }
  lines.push(current);
  return lines;
}

// Pairs deletions with the additions that follow them for the split layout.
function pairLines(lines) {
  const pairs = [];
  for (let i = 0; i < lines.length;) {
    if (lines[i].k === ' ') { pairs.push([lines[i], lines[i]]); i++; continue; }
    const dels = [];
    const adds = [];
    while (i < lines.length && lines[i].k === '-') dels.push(lines[i++]);
    while (i < lines.length && lines[i].k === '+') adds.push(lines[i++]);
    for (let j = 0; j < Math.max(dels.length, adds.length); j++) pairs.push([dels[j], adds[j]]);
  }
  return pairs;
}

// Line icons on a 16px grid, drawn with currentColor. Buttons in the page
// declare theirs with data-icon; everything else calls icon().
const ICONS = {
  chevron: '<path d="M6 3.5 10.5 8 6 12.5"/>',
  file: '<path d="M4 1.75h5.25l3 3v9.5H4z M9 1.75V5h3.25"/>',
  up: '<path d="M3.5 10 8 5.5l4.5 4.5"/>',
  down: '<path d="M3.5 6 8 10.5 12.5 6"/>',
  close: '<path d="m4 4 8 8M12 4l-8 8"/>',
  help: '<circle cx="8" cy="8" r="6.25"/><path d="M6.25 6.25a1.75 1.75 0 1 1 2.5 1.6c-.5.25-.75.6-.75 1.15v.5"/><circle cx="8" cy="11.5" r=".5" fill="currentColor" stroke="none"/>',
  panel: '<rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5"/><path d="M10 2.75v10.5"/>',
  inline: '<rect x="2.25" y="2.75" width="11.5" height="10.5" rx="1.5"/><path d="M4.5 6h7M4.5 8h7M4.5 10h5"/>',
  split: '<rect x="1.75" y="2.75" width="12.5" height="10.5" rx="1.5"/><path d="M8 2.75v10.5M3.75 6h2.5M3.75 8.5h2.5M9.75 6h2.5M9.75 8.5h2.5"/>',
  copy: '<rect x="5.25" y="5.25" width="8" height="8" rx="1.25"/><path d="M10.75 5.25v-1.5c0-.55-.45-1-1-1h-6c-.55 0-1 .45-1 1v6c0 .55.45 1 1 1h1.5"/>',
  agent: '<rect x="3.25" y="2.75" width="9.5" height="11" rx="1.25"/><path d="M6 2.75V2h4v.75M5.5 7h5M5.5 9.25h5M5.5 11.5h3"/>',
  edit: '<path d="M10.5 2.75 13.25 5.5 6 12.75H3.25V10z M9 4.25 11.75 7"/>',
  trash: '<path d="M2.75 4.25h10.5M6.25 4.25V2.75h3.5v1.5M4.25 4.25l.6 9h6.3l.6-9M6.75 6.75v4M9.25 6.75v4"/>',
  reply: '<path d="M6.25 3.5 2.75 7l3.5 3.5M2.75 7h6.5a4 4 0 0 1 4 4v1.5"/>',
  check: '<path d="m3 8.5 3.25 3.25L13 5"/>',
  reopen: '<path d="M3.25 8a4.75 4.75 0 1 0 1.4-3.36M3.25 2.75v2.5h2.5"/>',
};

function icon(name, size = 16) {
  if (!ICONS[name]) throw new Error(`unknown icon: ${name}`);
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('width', size);
  svg.setAttribute('height', size);
  svg.setAttribute('class', 'icon');
  svg.innerHTML = ICONS[name];
  return svg;
}

document.querySelectorAll('button[data-icon]').forEach(button => button.prepend(icon(button.dataset.icon)));
document.querySelectorAll('kbd[data-command]').forEach(kbd => { kbd.textContent = commandLabel(kbd.dataset.command); });

const KIND_CLASS = { '+': 'add', '-': 'del', ' ': '' };

function newNum(line, cls) {
  return el('div', { class: `num new ${cls}`, 'data-line': line.n - 1, title: 'Comment on this line (drag for a range)' }, String(line.n));
}

function plainRow(line, code) {
  return el('div', { class: 'row', 'data-key': `n${line.n}`, 'data-line': line.n - 1 },
    newNum(line, ''),
    code('text new-side', line, 'new'));
}

function inlineRow(line, code) {
  const cls = KIND_CLASS[line.k];
  return el('div', { class: 'row', 'data-key': line.n ? `n${line.n}` : `o${line.o}`, 'data-line': line.n ? line.n - 1 : null },
    el('div', { class: `num ${cls}` }, line.o ? String(line.o) : ''),
    line.n ? newNum(line, cls) : el('div', { class: `num ${cls}` }),
    el('div', { class: `sign ${cls}` }, line.k === ' ' ? '' : line.k),
    code(`text ${cls} new-side`, line, line.n ? 'new' : 'old'));
}

function splitRow(left, right, code) {
  const leftCls = left ? KIND_CLASS[left.k] : 'void';
  const rightCls = right ? KIND_CLASS[right.k] : 'void';
  return el('div', { class: 'row', 'data-key': right?.n ? `n${right.n}` : `o${left.o}`, 'data-line': right?.n ? right.n - 1 : null },
    el('div', { class: `num ${leftCls}` }, left ? String(left.o) : ''),
    code(`text ${leftCls}`, left, 'old'),
    right ? newNum(right, rightCls) : el('div', { class: `num ${rightCls}` }),
    code(`text ${rightCls} new-side`, right, 'new'));
}

function renderThread(root) {
  const thread = threadOf(root.id);
  const status = threadState(root.id);
  const composer = state.composer;
  const composing = composer && (composer.rootId === root.id || thread.some(c => c.id === composer.commentId));
  const picked = id => state.picker?.ids[state.picker.index] === id;
  const pickNum = id => state.picker?.ids.includes(id) && el('kbd', { class: 'pick-num' }, String(state.picker.ids.indexOf(id) + 1));
  const resolveButton = el('button', {
    class: 'resolve', title: `${root.resolved ? 'Reopen' : 'Done with this thread; stop showing it'} (s)`,
    onclick: event => { event.stopPropagation(); setResolved(root.id, !root.resolved); },
  }, icon(root.resolved ? 'reopen' : 'check'), root.resolved ? 'Unresolve' : 'Resolve');

  // A resolved thread shrinks to one line until it is opened.
  if (root.resolved && !state.openResolved.has(root.id) && !composing) {
    const replies = thread.length - 1;
    return el('div', { class: 'thread resolved collapsed', 'data-root': root.id },
      el('div', {
        class: `thread-head${picked(root.id) ? ' picked' : ''}`, 'data-comment': root.id, title: 'Show the resolved thread (zo)',
        onclick: () => { state.openResolved.add(root.id); renderView(); },
      },
      pickNum(root.id),
      el('span', { class: 'resolved-label' }, icon('check', 14), 'Resolved'),
      el('span', {}, lineLabel(root.line, root.endLine)),
      el('span', { class: 'preview' }, root.body.split('\n')[0]),
      replies > 0 && el('span', {}, `+${replies}`),
      resolveButton));
  }

  return el('div', { class: `thread ${status}`, 'data-root': root.id },
    el('div', {
      class: 'thread-head',
      onclick: root.resolved ? () => { state.openResolved.delete(root.id); renderView(); } : undefined,
      title: root.resolved ? 'Collapse (zc)' : undefined,
    },
    root.resolved && el('span', { class: 'resolved-label' }, icon('check', 14), 'Resolved'),
    lineLabel(root.line, root.endLine)),
    thread.map(comment => el('div', {
      class: `comment ${comment.source}${picked(comment.id) ? ' picked' : ''}`,
      'data-comment': comment.id,
    },
      el('div', {},
        el('div', { class: 'meta' },
          pickNum(comment.id),
          el('span', { class: 'author' }, comment.author),
          el('time', { datetime: comment.createdAt, title: new Date(comment.createdAt).toLocaleString() }, formatTime(comment.createdAt)),
          el('span', { class: 'actions' },
            el('button', { class: 'ghost', title: 'Copy comment ID', onclick: () => copyText(comment.id, 'Copied comment ID') }, icon('copy'), 'ID'),
            el('button', { class: 'ghost square', title: 'Edit (e)', onclick: () => startComposer({ kind: 'edit', commentId: comment.id }) }, icon('edit')),
            el('button', { class: 'ghost square danger-text', title: 'Delete (x)', onclick: () => removeComment(comment.id) }, icon('trash')))),
        composer?.kind === 'edit' && composer.commentId === comment.id
          ? composerEl
          : el('div', { class: 'body' }, comment.body)))),
    composer?.kind === 'reply' && composer.rootId === root.id
      ? composerEl
      : el('div', { class: 'thread-foot' },
        el('button', { title: root.resolved ? 'Reply (r); reopens the thread' : 'Reply (r)', onclick: () => startComposer({ kind: 'reply', rootId: root.id }) }, icon('reply'), 'Reply'),
        el('span', { class: 'spacer' }),
        resolveButton));
}

// Keeps the row at the top of the viewport in place across re-renders.
function captureAnchor() {
  const content = $('#content');
  const rect = content.getBoundingClientRect();
  const row = document.elementFromPoint(rect.left + rect.width / 2, rect.top + 2)?.closest('.row, .fold');
  if (!row) return { scrollTop: content.scrollTop };
  return { key: row.dataset.key, offset: row.getBoundingClientRect().top - rect.top, scrollTop: content.scrollTop };
}

function restoreAnchor(anchor) {
  const content = $('#content');
  const row = anchor.key && content.querySelector(`[data-key="${anchor.key}"]`);
  if (!row) { content.scrollTop = anchor.scrollTop; return; }
  content.scrollTop += row.getBoundingClientRect().top - content.getBoundingClientRect().top - anchor.offset;
}

function scrollRowIntoView(row, offset = 96) {
  const content = $('#content');
  content.scrollTop += row.getBoundingClientRect().top - content.getBoundingClientRect().top - offset;
}

function scrollToPendingTarget() {
  const target = state.pendingTarget;
  if (!target || !state.view) return;
  state.pendingTarget = null;
  state.visual = null;
  state.selection = null;
  setCursor(target.line, false);
  const row = rowOfLine(target.line);
  if (!row) return;
  scrollRowIntoView(row, $('#content').clientHeight / 3);
  for (let line = target.line; line <= target.endLine; line++) {
    const flash = rowOfLine(line);
    if (!flash) continue;
    flash.classList.remove('flash');
    void flash.offsetWidth; // restart the animation
    flash.classList.add('flash');
  }
}

// ---- Cursor -----------------------------------------------------------------
// The cursor stops on rows that have a new side, holding that zero-based line
// like the lines comments attach to, and on folded runs, holding the fold's
// key with no line. Deleted-only rows are skipped.

const cursorRows = () => [...document.querySelectorAll('#content .row[data-line], #content .fold')];
const rowOfLine = line => $(`#content .row[data-line="${line}"]`);
const isCursorRow = row => row.classList.contains('fold')
  ? row.dataset.key === state.cursorFold
  : Number(row.dataset.line) === state.cursor;
const cursorRow = () => cursorRows().find(isCursorRow) ?? null;

function setCursorRow(row, reveal = true) {
  if (!row.classList.contains('fold')) { setCursor(Number(row.dataset.line), reveal); return; }
  state.cursor = null;
  state.cursorFold = row.dataset.key;
  applySelection();
  if (reveal) revealCursor();
}

// Shows the folded run and keeps the cursor where the fold was, on the first
// revealed line.
function expandFold(fold) {
  const position = cursorRows().indexOf(fold);
  const hadCursor = isCursorRow(fold);
  state.expanded.add(fold.runKey);
  renderView();
  if (hadCursor) setCursorRow(cursorRows()[position]);
}

function setCursor(line, reveal = true) {
  state.cursor = line;
  state.cursorFold = null;
  if (state.visual !== null) {
    state.selection = { start: Math.min(state.visual, line), end: Math.max(state.visual, line) };
  }
  applySelection();
  if (reveal) revealCursor();
}

function revealCursor() {
  const row = cursorRow();
  if (!row) return;
  const content = $('#content');
  const box = row.getBoundingClientRect();
  const view = content.getBoundingClientRect();
  const margin = 60;
  if (box.top < view.top + margin) content.scrollTop -= view.top + margin - box.top;
  else if (box.bottom > view.bottom - margin) content.scrollTop += box.bottom - view.bottom + margin;
}

function moveCursor(delta) {
  const rows = cursorRows();
  if (!rows.length) return;
  let i = rows.findIndex(isCursorRow);
  if (i < 0) {
    // No cursor yet: start at the first row on screen.
    const top = $('#content').getBoundingClientRect().top;
    i = Math.max(0, rows.findIndex(row => row.getBoundingClientRect().top >= top));
  } else {
    i = Math.max(0, Math.min(rows.length - 1, i + delta));
  }
  setCursorRow(rows[i]);
}

function moveCursorTo(position) {
  const rows = cursorRows();
  if (!rows.length) return;
  setCursorRow(rows[Math.min(position, rows.length - 1)]);
}

// Moves the cursor to the |count|-th next (count > 0) or previous block of
// changed lines; `fromTop` picks the first block of a freshly opened file.
function jumpToChange(count, fromTop = false) {
  const content = $('#content');
  const all = [...content.querySelectorAll('.row')];
  const starts = all.filter(row => row.classList.contains('change-start'));
  let target;
  if (fromTop) {
    target = starts[0];
    if (!target) { content.scrollTop = 0; moveCursorTo(0); return; }
  } else {
    const here = all.indexOf(rowOfLine(state.cursor));
    const top = content.getBoundingClientRect().top + 96;
    // Without a cursor, measure from the scroll position.
    const after = here >= 0
      ? row => all.indexOf(row) > here
      : row => row.getBoundingClientRect().top > top + 4;
    const candidates = count > 0 ? starts.filter(after) : starts.filter(row => !after(row) && row !== rowOfLine(state.cursor)).reverse();
    target = candidates[Math.min(Math.abs(count), candidates.length) - 1];
    if (!target) { toast(count > 0 ? 'No more changes below' : 'No more changes above'); return; }
  }
  scrollRowIntoView(target);
  // A block may start with deleted lines; the cursor goes to its first new-side line.
  const landing = all.slice(all.indexOf(target)).find(row => row.dataset.line !== undefined);
  if (landing) setCursor(Number(landing.dataset.line), false);
}

// Threads in the panel's current filter, in path and line order.
function navigableThreads() {
  return roots()
    .filter(inFilter)
    .sort((a, b) => a.path.localeCompare(b.path) || a.line - b.line);
}

// Jumps to the next (+1) or previous (-1) thread across files, wrapping.
function jumpToThread(direction) {
  const threads = navigableThreads();
  if (!threads.length) { toast(`No ${state.threadFilter === 'all' ? '' : state.threadFilter + ' '}threads`); return; }
  const path = state.selected ?? '';
  const line = state.cursor ?? -1;
  const compare = t => t.path.localeCompare(path) || t.line - line;
  let target = direction > 0 ? threads.find(t => compare(t) > 0) : threads.findLast(t => compare(t) < 0);
  if (!target) {
    target = direction > 0 ? threads[0] : threads.at(-1);
    toast(direction > 0 ? 'Wrapped to the first thread' : 'Wrapped to the last thread');
  }
  state.hiddenThreads.delete(target.id);
  openFile(target.path, { line: target.line, endLine: target.endLine, commentId: target.id });
}

// The threads covering the cursor line, outermost first.
function threadsAtCursor() {
  if (state.cursor === null || !state.view) return [];
  const hits = roots().filter(root => root.path === state.view.path && root.line <= state.cursor && state.cursor <= root.endLine);
  if (!hits.length) toast('No thread on this line');
  return hits.sort((a, b) => a.line - b.line || b.endLine - a.endLine);
}

// zo / zc open or close a thread on the current line; zR / zm all threads.
async function foldThreads(key) {
  if (key === 'm') { roots().forEach(root => state.hiddenThreads.add(root.id)); state.openResolved.clear(); }
  else if (key === 'R') state.hiddenThreads.clear();
  else {
    const root = await chooseThread(key === 'o' ? 'Show' : 'Hide');
    if (!root) return;
    // A resolved thread opens from hidden to collapsed to expanded, and
    // closes the other way.
    if (key === 'o') {
      if (!state.hiddenThreads.delete(root.id) && root.resolved) state.openResolved.add(root.id);
    } else if (!state.openResolved.delete(root.id)) state.hiddenThreads.add(root.id);
  }
  renderView();
}

// ---- Choosing (dd, r, x, e, s, zo, zc) --------------------------------------------
// When the current line has several candidates (threads on the line, or
// comments in a thread), they get numbers: press one, or move with j / k and
// press Enter. A lone candidate is taken directly. Threads are numbered on
// their first comment.

let pickerResolve = null;

function choose(comments, prompt) {
  if (comments.length <= 1) return Promise.resolve(comments[0] || null);
  comments.forEach(comment => state.hiddenThreads.delete(comment.parentId || comment.id));
  renderView();
  // Number in screen order so 1 is the topmost candidate.
  const shown = [...document.querySelectorAll('#content [data-comment]')].map(node => node.dataset.comment);
  const ids = comments.map(comment => comment.id).sort((a, b) => shown.indexOf(a) - shown.indexOf(b));
  if (ids.some(id => !shown.includes(id))) throw new Error(`choice not on screen: ${ids}`);
  state.picker = { ids, index: ids.length - 1 };
  renderView();
  $(`#content [data-comment="${ids[0]}"]`)?.scrollIntoView({ block: 'nearest' });
  toast(`${prompt} 1–${comments.length}, j / k + Enter, Esc to cancel`);
  return new Promise(resolve => {
    pickerResolve = id => resolve(comments.find(comment => comment.id === id) || null);
  });
}

function chooseThread(verb) {
  return choose(threadsAtCursor(), `${verb} which thread?`);
}

async function chooseComment(verb) {
  const root = await chooseThread(verb);
  if (!root) return null;
  state.hiddenThreads.delete(root.id);
  if (root.resolved) state.openResolved.add(root.id);
  return choose(threadOf(root.id), `${verb} which comment?`);
}

function pickerKey(event) {
  const picker = state.picker;
  event.preventDefault();
  let chosen;
  if (/^[1-9]$/.test(event.key) && Number(event.key) <= picker.ids.length) chosen = picker.ids[Number(event.key) - 1];
  else if (event.key === 'Enter') chosen = picker.ids[picker.index];
  else if (event.key === 'Escape') chosen = null;
  else {
    if (event.key === 'j' || event.key === 'ArrowDown') picker.index = Math.min(picker.ids.length - 1, picker.index + 1);
    else if (event.key === 'k' || event.key === 'ArrowUp') picker.index = Math.max(0, picker.index - 1);
    else return;
    state.picker = { ...picker };
    renderView();
    return;
  }
  state.picker = null;
  renderView();
  const resolve = pickerResolve;
  pickerResolve = null;
  resolve(chosen);
}

async function editAtCursor() {
  const comment = await chooseComment('Edit');
  if (comment) startComposer({ kind: 'edit', commentId: comment.id });
}

async function replyAtCursor() {
  const root = await chooseThread('Reply to');
  if (!root) return;
  state.hiddenThreads.delete(root.id);
  startComposer({ kind: 'reply', rootId: root.id });
}

async function toggleResolvedAtCursor() {
  const hits = threadsAtCursor();
  const root = await choose(hits, hits.some(hit => !hit.resolved) ? 'Resolve or reopen which thread?' : 'Unresolve which thread?');
  if (root) await setResolved(root.id, !root.resolved);
}

async function deleteCommentAtCursor() {
  const comment = await chooseComment('Delete');
  if (!comment) return;
  const size = threadOf(comment.parentId || comment.id).length;
  const text = !comment.parentId && size > 1
    ? `Delete the first comment? It starts the thread, so all ${size} comments go with it.`
    : `Delete this comment by ${comment.author}?`;
  if (await dialog(text, [{ label: 'Delete', value: true, class: 'destructive' }])) await removeComment(comment.id);
}

async function deleteThreadAtCursor() {
  const root = await chooseThread('Delete');
  if (!root) return;
  const choice = await dialog(`Delete the thread on ${lineLabel(root.line, root.endLine)} (${threadOf(root.id).length} comment(s))?`,
    [{ label: 'Delete', value: true, class: 'destructive' }]);
  if (choice) await removeComment(root.id);
}

// ---- Line selection ---------------------------------------------------------

function applySelection() {
  const selection = state.selection;
  for (const row of document.querySelectorAll('#content .row[data-line]')) {
    const line = Number(row.dataset.line);
    row.classList.toggle('selected', !!selection && line >= selection.start && line <= selection.end);
    row.classList.toggle('cursor', line === state.cursor);
  }
  for (const fold of document.querySelectorAll('#content .fold')) fold.classList.toggle('cursor', isCursorRow(fold));
}

$('#content').addEventListener('mousedown', event => {
  const num = event.target.closest('.num.new');
  if (!num || event.button !== 0) return;
  event.preventDefault();
  const line = Number(num.dataset.line);
  const anchor = event.shiftKey && state.selection ? state.selection.start : line;
  state.drag = { anchor };
  state.cursor = line;
  state.cursorFold = null;
  state.selection = { start: Math.min(anchor, line), end: Math.max(anchor, line) };
  applySelection();
});

$('#content').addEventListener('mouseover', event => {
  if (!state.drag) return;
  const row = event.target.closest('.row[data-line]');
  if (!row) return;
  const line = Number(row.dataset.line);
  state.selection = { start: Math.min(state.drag.anchor, line), end: Math.max(state.drag.anchor, line) };
  applySelection();
});

// Clicking code moves the cursor there.
$('#content').addEventListener('click', event => {
  if (event.target.closest('.num.new, .thread, .composer')) return;
  const row = event.target.closest('.row[data-line]');
  if (row) setCursor(Number(row.dataset.line), false);
});

document.addEventListener('mouseup', () => {
  if (!state.drag) return;
  state.drag = null;
  startComposer({ kind: 'new', line: state.selection.start, endLine: state.selection.end });
});

// ---- Composer ---------------------------------------------------------------
// One composer exists at a time. Its DOM node is reused across re-renders so
// that live updates never lose what the user is typing.

const composerEl = el('div', { class: 'composer' },
  el('div', { class: 'title' }),
  el('textarea', { spellcheck: 'false' }),
  el('div', { class: 'actions' },
    el('span', { class: 'hint' }, `${commandLabel('Enter')} to submit · Esc to cancel`),
    el('button', { class: 'ghost', onclick: () => closeComposer() }, 'Cancel'),
    el('button', { class: 'primary', onclick: () => submitComposer() }, 'Comment')));
const composerText = composerEl.querySelector('textarea');

composerText.addEventListener('keydown', event => {
  if (event.isComposing) return;
  if (event.key === 'Enter' && commandKey(event)) { event.preventDefault(); submitComposer(); }
  if (event.key === 'Escape') { event.preventDefault(); closeComposer(); }
});

function startComposer(composer) {
  state.composer = composer;
  const title = composerEl.querySelector('.title');
  const submit = composerEl.querySelector('.primary');
  composerEl.classList.toggle('new', composer.kind === 'new');
  title.hidden = composer.kind !== 'new';
  if (composer.kind === 'new') {
    title.textContent = `New thread on ${lineLabel(composer.line, composer.endLine)}`;
    composerText.placeholder = 'What should the agent look at?';
    submit.textContent = 'Comment';
    composerText.value = '';
  } else if (composer.kind === 'reply') {
    composerText.placeholder = 'Reply…';
    submit.textContent = 'Reply';
    composerText.value = '';
  } else {
    submit.textContent = 'Save';
    composerText.value = state.comments.find(c => c.id === composer.commentId).body;
  }
  renderView();
  composerText.focus();
}

function closeComposer() {
  if (state.composer?.kind === 'new') state.selection = null;
  state.visual = null;
  state.composer = null;
  composerText.blur();
  renderView();
}

async function submitComposer() {
  const composer = state.composer;
  const body = composerText.value;
  if (!body.trim()) return;
  if (composer.kind === 'new') {
    await api('POST', '/v1/comments', { path: state.view.path, line: composer.line, endLine: composer.endLine, body, source: 'human' });
  } else if (composer.kind === 'reply') {
    const root = state.comments.find(c => c.id === composer.rootId);
    await api('POST', `/v1/comments/${root.id}/replies`, { path: root.path, line: root.line, endLine: root.endLine, body, source: 'human' });
  } else {
    await api('PATCH', `/v1/comments/${composer.commentId}`, { body });
  }
  // Only discard the text once the server has it.
  composerText.value = '';
  composerText.blur();
  state.composer = null;
  state.selection = null;
  state.visual = null;
  await refreshComments();
}

function captureComposerFocus() {
  if (document.activeElement !== composerText) return null;
  return { start: composerText.selectionStart, end: composerText.selectionEnd };
}

function restoreComposerFocus(focus) {
  if (!focus || !composerEl.isConnected) return;
  composerText.focus();
  composerText.setSelectionRange(focus.start, focus.end);
}

async function setResolved(rootId, resolved) {
  state.openResolved.delete(rootId);
  await api('PUT', `/v1/comments/${rootId}/resolved`, { resolved });
  await refreshComments();
  toast(resolved ? 'Resolved' : 'Reopened');
}

async function removeComment(id) {
  await api('DELETE', `/v1/comments/${id}`);
  await refreshComments();
}

// ---- Review panel -----------------------------------------------------------

let overallSent = '';
let overallTimer;
let overallComposing = false;
const overall = $('#overall');

async function sendOverall() {
  clearTimeout(overallTimer);
  if (overallComposing || overall.value === overallSent) return;
  const value = overall.value;
  await api('PUT', '/ui/overall', { overall: value });
  overallSent = value;
}
function scheduleOverall() {
  clearTimeout(overallTimer);
  if (!overallComposing) overallTimer = setTimeout(sendOverall, 300);
}
overall.addEventListener('input', scheduleOverall);
overall.addEventListener('compositionstart', () => { overallComposing = true; clearTimeout(overallTimer); });
overall.addEventListener('compositionend', () => { overallComposing = false; scheduleOverall(); });
overall.addEventListener('blur', sendOverall);

$('#copy-instructions').addEventListener('click', async () => {
  const { text } = await api('GET', '/ui/instructions');
  await copyText(text, 'Copied agent instructions — paste them into your agent');
});
$('#clear-review').addEventListener('click', async () => {
  const choice = await dialog('Start over? Deleted comments cannot be restored.', [
    { label: 'Delete comments', value: 'comments', class: 'destructive' },
    { label: 'Clear everything', value: 'review', class: 'destructive' },
  ]);
  if (choice === 'comments') await api('DELETE', '/v1/comments');
  if (choice === 'review') {
    await api('DELETE', '/ui/review');
    overall.value = overallSent = '';
  }
  if (choice) await refreshComments();
});

// ---- Chrome & keyboard ------------------------------------------------------

$('#panel-button').addEventListener('click', () => document.body.classList.toggle('panel-hidden'));
$('#help-button').addEventListener('click', () => { $('#help').hidden = false; });
$('#help-close').addEventListener('click', () => { $('#help').hidden = true; });
$('#help').addEventListener('mousedown', event => {
  if (event.target === event.currentTarget) event.currentTarget.hidden = true;
});

for (const resizer of document.querySelectorAll('.resizer')) {
  resizer.addEventListener('mousedown', event => {
    event.preventDefault();
    resizer.classList.add('active');
    const left = resizer.dataset.side === 'left';
    const move = e => {
      const width = left ? e.clientX : window.innerWidth - e.clientX;
      document.documentElement.style.setProperty(left ? '--left-w' : '--right-w', `${Math.max(200, Math.min(width, window.innerWidth / 2))}px`);
    };
    const up = () => {
      resizer.classList.remove('active');
      document.removeEventListener('mousemove', move);
      document.removeEventListener('mouseup', up);
    };
    document.addEventListener('mousemove', move);
    document.addEventListener('mouseup', up);
  });
}

// Vim-like normal mode. A count prefix repeats motions (5j); two-key
// commands (gg, dd) must be completed within a short delay.
const keys = { count: '', pending: null, pendingAt: 0 };

document.addEventListener('keydown', event => {
  if (event.key === 'Escape' && !$('#help').hidden) { $('#help').hidden = true; return; }
  if (commandKey(event) && event.key.toLowerCase() === 'e') { event.preventDefault(); openFinder(); return; }
  if (commandKey(event) && event.key.toLowerCase() === 'f') { event.preventDefault(); openSearch(); return; }
  if (openDialog) { dialogKey(event); return; }
  if (state.picker) { pickerKey(event); return; }
  if (!$('#finder').hidden || typing() || event.metaKey || event.altKey) return;
  if (event.ctrlKey) {
    if (event.key === 'n' || event.key === 'p') {
      event.preventDefault();
      jumpToThread(event.key === 'n' ? 1 : -1);
    }
    if (event.key === 'd' || event.key === 'u') {
      event.preventDefault();
      moveCursor((event.key === 'd' ? 1 : -1) * Math.max(1, Math.floor($('#content').clientHeight / 2 / 20)));
    }
    return;
  }

  // Every plain key is consumed here: an unconsumed key makes macOS beep.
  // Keys with native behavior worth keeping (arrows, Tab, ...) pass through.
  if (event.key.length === 1 || event.key === 'Escape') event.preventDefault();
  if (/^[0-9]$/.test(event.key) && !(event.key === '0' && keys.count === '')) {
    keys.count += event.key;
    return;
  }
  const count = Math.max(1, Number(keys.count || '1'));
  keys.count = '';
  const pending = Date.now() - keys.pendingAt < 800 ? keys.pending : null;
  keys.pending = null;

  const files = orderedFiles();
  const index = files.findIndex(f => f.path === state.selected);
  switch (event.key) {
    case 'j': moveCursor(count); break;
    case 'k': moveCursor(-count); break;
    case 'G': moveCursorTo(Infinity); break;
    case 'g':
      if (pending === 'g') moveCursorTo(0);
      else { keys.pending = 'g'; keys.pendingAt = Date.now(); }
      break;
    case '}':
    case '{': {
      const next = files[event.key === '}' ? index + count : (index < 0 ? files.length : index) - count];
      if (next) openFile(next.path);
      break;
    }
    case ']': jumpToChange(count); break;
    case '[': jumpToChange(-count); break;
    case 'n': stepSearch(count); break;
    case 'p':
    case 'N': stepSearch(-count); break;
    case 'V':
      if (state.cursor === null) break;
      state.visual = state.visual === null ? state.cursor : null;
      state.selection = state.visual === null ? null : { start: state.cursor, end: state.cursor };
      applySelection();
      break;
    case 'a': {
      const range = state.selection ?? (state.cursor === null ? null : { start: state.cursor, end: state.cursor });
      if (!range) break;
      event.preventDefault(); // keep the key out of the composer
      state.visual = null;
      state.selection = range;
      startComposer({ kind: 'new', line: range.start, endLine: range.end });
      break;
    }
    case 'd':
      if (pending === 'd') deleteThreadAtCursor();
      else { keys.pending = 'd'; keys.pendingAt = Date.now(); }
      break;
    case 'x': deleteCommentAtCursor(); break;
    case 'r': replyAtCursor(); break;
    case 'e': editAtCursor(); break;
    case 's': toggleResolvedAtCursor(); break;
    case 'z':
      keys.pending = 'z';
      keys.pendingAt = Date.now();
      break;
    case 'Enter':
      if (state.cursorFold && cursorRow()) expandFold(cursorRow());
      break;
    case 'o':
      if (pending === 'z' && state.cursorFold && cursorRow()) { expandFold(cursorRow()); break; }
      // fall through
    case 'c':
    case 'm':
    case 'R':
      if (pending !== 'z') break;
      foldThreads(event.key);
      break;
    case ' ':
      event.preventDefault(); // not a page scroll
      if (state.session.diff && index >= 0) setReviewed(files[index], !isReviewed(files[index]));
      break;
    case '/': event.preventDefault(); openSearch(); break;
    case '?': $('#help').hidden = false; break;
    case 'Escape':
      if (state.selection) {
        state.visual = null;
        state.selection = null;
        applySelection();
      } else if (!$('#search').hidden) {
        closeSearch();
      }
      break;
  }
});

// ---- Startup ----------------------------------------------------------------

function connectEvents() {
  const events = new EventSource('/ui/events');
  events.addEventListener('open', () => refreshComments());
  events.addEventListener('state', () => refreshComments());
  events.addEventListener('navigate', event => {
    const target = JSON.parse(event.data);
    openFile(target.path, target);
  });
  events.addEventListener('error', () => showError('Lost connection to the review-relay process. Retrying…'));
}

(async () => {
  await loadSession();
  await Promise.all([refreshFiles(), refreshComments()]);
  if (state.session.diff && state.files.length) await openFile(orderedFiles()[0].path);
  else renderView();
  connectEvents();
  pollLoop();
})();
