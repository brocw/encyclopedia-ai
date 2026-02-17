// DOM Elements
const topicInput = document.getElementById('topicInput');
const maxRoundsInput = document.getElementById('maxRoundsInput');
const startButton = document.getElementById('startButton');
const loadingIndicator = document.getElementById('loading');
const mainContent = document.getElementById('mainContent');
const articleEl = document.getElementById('article');
const topicEl = document.getElementById('topic');
const infoboxEl = document.getElementById('infobox');
const seeAlsoEl = document.getElementById('seeAlso');
const referencesEl = document.getElementById('references');
const categoriesEl = document.getElementById('articleCategories');
const roundTimelineEl = document.getElementById('roundTimeline');
const convergenceBadge = document.getElementById('convergenceBadge');
const roundCounter = document.getElementById('roundCounter');

// State
let articleState = null;

// --- Event Listeners ---
startButton.addEventListener('click', handleStart);

// --- JSON Renderers ---

function renderInfoboxJSON(jsonStr) {
    const data = JSON.parse(jsonStr);
    let html = '<table>';
    for (const row of data.rows) {
        html += `<tr><th>${escapeHTML(row.field)}</th><td>${escapeHTML(row.value)}</td></tr>`;
    }
    html += '</table>';
    return html;
}

function renderReferencesJSON(jsonStr) {
    const data = JSON.parse(jsonStr);
    let html = '<ol>';
    for (const ref of data.references) {
        const parts = [];
        if (ref.author) parts.push(escapeHTML(ref.author));
        if (ref.title) parts.push(`<i>${escapeHTML(ref.title)}</i>`);
        if (ref.publisher) parts.push(escapeHTML(ref.publisher));
        if (ref.year) parts.push(escapeHTML(ref.year));
        html += `<li>${parts.join('. ')}.</li>`;
    }
    html += '</ol>';
    return html;
}

function renderSeeAlsoJSON(jsonStr) {
    const data = JSON.parse(jsonStr);
    let html = '<ul>';
    for (const topic of data.topics) {
        html += `<li>${escapeHTML(topic)}</li>`;
    }
    html += '</ul>';
    return html;
}

function renderCategoriesJSON(jsonStr) {
    const data = JSON.parse(jsonStr);
    return data.categories.map(c => escapeHTML(c)).join(', ');
}

function escapeHTML(str) {
    const div = document.createElement('div');
    div.textContent = str;
    return div.innerHTML;
}

// --- Score color helper ---

function scoreColor(score) {
    if (score >= 8) return 'score-green';
    if (score >= 5) return 'score-yellow';
    return 'score-red';
}

// --- Phase status helper ---

function setPhaseStatus(phaseId, status) {
    const el = document.getElementById(phaseId);
    if (!el) return;
    const span = el.querySelector('.agent-status');
    span.className = 'agent-status ' + status;
    el.classList.remove('is-active', 'is-done');
    if (status === 'active') el.classList.add('is-active');
    if (status === 'done') el.classList.add('is-done');
}

function resetPhases() {
    ['phase-generate', 'phase-evaluate', 'phase-plan', 'phase-revise', 'phase-metadata']
        .forEach(id => setPhaseStatus(id, 'pending'));
    roundCounter.textContent = '';
}

// --- Functions ---

/**
 * Reads an SSE stream from a fetch response and dispatches token events.
 * Returns the final ArticleState from the "done" event.
 */
async function streamSSE(response, callbacks) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let currentEvent = '';
    let result = null;

    const eventMap = {
        article_token: callbacks.onArticleToken,
        evaluation_token: callbacks.onEvaluationToken,
        revision_plan_token: callbacks.onRevisionPlanToken,
        references_token: callbacks.onReferencesToken,
        infobox_token: callbacks.onInfoboxToken,
        seealso_token: callbacks.onSeeAlsoToken,
        category_token: callbacks.onCategoryToken,
    };

    while (true) {
        const { done, value } = await reader.read();
        if (done) break;

        buffer += decoder.decode(value, { stream: true });
        const lines = buffer.split('\n');
        buffer = lines.pop();

        for (const line of lines) {
            if (line.startsWith('event: ')) {
                currentEvent = line.slice(7);
            } else if (line.startsWith('data: ')) {
                const raw = line.slice(6);
                const handler = eventMap[currentEvent];
                if (handler) {
                    handler(JSON.parse(raw));
                } else if (currentEvent === 'round_complete') {
                    if (callbacks.onRoundComplete) {
                        callbacks.onRoundComplete(JSON.parse(raw));
                    }
                } else if (currentEvent === 'converged') {
                    if (callbacks.onConverged) callbacks.onConverged();
                } else if (currentEvent === 'article_done') {
                    setPhaseStatus('phase-metadata', 'active');
                } else if (currentEvent === 'done') {
                    result = JSON.parse(JSON.parse(raw));
                    setPhaseStatus('phase-metadata', 'done');
                } else if (currentEvent === 'error') {
                    throw new Error(JSON.parse(raw));
                }
                currentEvent = '';
            }
        }
    }

    return result;
}

/**
 * Handles the "Generate" button click — triggers the full cybernetic loop.
 */
async function handleStart() {
    const topic = topicInput.value.trim();
    if (!topic) {
        alert('Please enter a topic.');
        return;
    }

    const maxRounds = parseInt(maxRoundsInput.value) || 3;

    setLoading(true);
    resetPhases();
    setPhaseStatus('phase-generate', 'active');
    mainContent.classList.remove('hidden');
    topicEl.textContent = topic;
    clearContent();

    let articleText = '';
    let currentRound = 0;
    let articleIsRevision = false;
    let referencesText = '';
    let infoboxText = '';
    let seeAlsoText = '';
    let categoryText = '';

    try {
        const response = await fetch('/api/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ topic, max_rounds: maxRounds }),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await streamSSE(response, {
            onArticleToken(token) {
                // The article_token event fires for both initial generation and revisions.
                // On the first revision, we need to clear the previous article text.
                if (articleIsRevision) {
                    articleText = '';
                    articleIsRevision = false;
                    setPhaseStatus('phase-revise', 'active');
                }
                articleText += token;
                articleEl.innerHTML = marked.parse(articleText);
                debouncedTOCUpdate();
            },
            onEvaluationToken(token) {
                setPhaseStatus('phase-generate', 'done');
                setPhaseStatus('phase-evaluate', 'active');
            },
            onRevisionPlanToken(token) {
                setPhaseStatus('phase-evaluate', 'done');
                setPhaseStatus('phase-plan', 'active');
            },
            onRoundComplete(round) {
                currentRound = round.number;
                roundCounter.textContent = `Round ${currentRound} complete (score: ${round.evaluation.overall.toFixed(1)})`;
                addRoundToTimeline(round);

                // Prepare for next revision
                setPhaseStatus('phase-plan', 'done');
                setPhaseStatus('phase-revise', 'pending');
                articleIsRevision = true;
            },
            onConverged() {
                convergenceBadge.textContent = 'Converged';
                convergenceBadge.className = 'convergence-badge converged';
            },
            onReferencesToken(token) {
                referencesText += token;
                document.getElementById('references-section').classList.remove('hidden');
            },
            onInfoboxToken(token) {
                infoboxText += token;
                infoboxEl.classList.remove('hidden');
            },
            onSeeAlsoToken(token) {
                seeAlsoText += token;
                document.getElementById('seealso-section').classList.remove('hidden');
            },
            onCategoryToken(token) {
                categoryText += token;
            },
        });

        render();

    } catch (error) {
        alert(`Failed to generate article: ${error.message}`);
    } finally {
        setLoading(false);
    }
}

/**
 * Adds a completed round to the visual timeline.
 */
function addRoundToTimeline(round) {
    const timelineSection = document.getElementById('round-timeline');
    timelineSection.classList.remove('hidden');

    const el = document.createElement('div');
    el.className = 'round-card';

    const scores = round.evaluation.scores;
    const overall = round.evaluation.overall;

    let issuesHTML = '';
    if (round.evaluation.critical_issues && round.evaluation.critical_issues.length > 0) {
        issuesHTML = '<ul class="round-issues">' +
            round.evaluation.critical_issues.map(i => `<li>${escapeHTML(i)}</li>`).join('') +
            '</ul>';
    }

    el.innerHTML = `
        <div class="round-header">
            <span class="round-number">Round ${round.number}</span>
            <span class="round-overall ${scoreColor(overall)}">${overall.toFixed(1)}</span>
        </div>
        <div class="round-scores">
            <span class="${scoreColor(scores.factual_accuracy)}">Accuracy: ${scores.factual_accuracy}</span>
            <span class="${scoreColor(scores.completeness)}">Completeness: ${scores.completeness}</span>
            <span class="${scoreColor(scores.neutrality)}">Neutrality: ${scores.neutrality}</span>
            <span class="${scoreColor(scores.clarity)}">Clarity: ${scores.clarity}</span>
            <span class="${scoreColor(scores.structure)}">Structure: ${scores.structure}</span>
        </div>
        ${issuesHTML}
    `;

    roundTimelineEl.appendChild(el);
}

/**
 * Clears all content areas for a fresh render.
 */
function clearContent() {
    articleEl.innerHTML = '';
    infoboxEl.innerHTML = '';
    infoboxEl.classList.add('hidden');
    seeAlsoEl.innerHTML = '';
    document.getElementById('seealso-section').classList.add('hidden');
    referencesEl.innerHTML = '';
    document.getElementById('references-section').classList.add('hidden');
    categoriesEl.textContent = '';
    roundTimelineEl.innerHTML = '';
    document.getElementById('round-timeline').classList.add('hidden');
    convergenceBadge.className = 'convergence-badge hidden';
    convergenceBadge.textContent = '';
}

/**
 * Updates the UI based on the current articleState.
 */
function render() {
    if (!articleState) return;

    topicEl.textContent = articleState.topic;
    articleEl.innerHTML = marked.parse(articleState.current_article);

    if (articleState.infobox) {
        try {
            infoboxEl.innerHTML = renderInfoboxJSON(articleState.infobox);
        } catch {
            infoboxEl.innerHTML = marked.parse(articleState.infobox);
        }
        infoboxEl.classList.remove('hidden');
    }

    if (articleState.see_also) {
        try {
            seeAlsoEl.innerHTML = renderSeeAlsoJSON(articleState.see_also);
        } catch {
            seeAlsoEl.innerHTML = marked.parse(articleState.see_also);
        }
        document.getElementById('seealso-section').classList.remove('hidden');
    }

    if (articleState.references) {
        try {
            referencesEl.innerHTML = renderReferencesJSON(articleState.references);
        } catch {
            referencesEl.innerHTML = marked.parse(articleState.references);
        }
        document.getElementById('references-section').classList.remove('hidden');
    }

    if (articleState.categories) {
        try {
            categoriesEl.textContent = renderCategoriesJSON(articleState.categories);
        } catch {
            categoriesEl.textContent = articleState.categories;
        }
    }

    // Render round timeline from final state
    if (articleState.rounds && articleState.rounds.length > 0) {
        roundTimelineEl.innerHTML = '';
        document.getElementById('round-timeline').classList.remove('hidden');
        for (const round of articleState.rounds) {
            addRoundToTimeline(round);
        }
    }

    // Update convergence badge
    if (articleState.converged) {
        convergenceBadge.textContent = 'Converged';
        convergenceBadge.className = 'convergence-badge converged';
    } else if (articleState.rounds && articleState.rounds.length > 0) {
        convergenceBadge.textContent = 'Max rounds reached';
        convergenceBadge.className = 'convergence-badge not-converged';
    }

    mainContent.classList.remove('hidden');

    generateTOC();
    addEditSectionLinks();
}

/**
 * Manages the visibility of the loading indicator and disables buttons.
 */
function setLoading(isLoading) {
    if (isLoading) {
        loadingIndicator.classList.remove('hidden');
        startButton.disabled = true;
    } else {
        loadingIndicator.classList.add('hidden');
        startButton.disabled = false;
    }
}

// --- Table of Contents ---

const tocEl = document.getElementById('tableOfContents');

function generateTOC() {
    const headings = articleEl.querySelectorAll('h1, h2, h3, h4');

    if (headings.length === 0) {
        tocEl.innerHTML = '<p class="toc-placeholder">Generate an article to see contents.</p>';
        return;
    }

    let html = '<ul>';
    headings.forEach((heading, index) => {
        const id = 'section-' + index;
        heading.id = id;
        const level = heading.tagName.toLowerCase();
        html += `<li class="toc-${level}"><a href="#${id}">${heading.textContent}</a></li>`;
    });
    html += '</ul>';
    tocEl.innerHTML = html;
}

function addEditSectionLinks() {
    const headings = articleEl.querySelectorAll('h1, h2, h3');
    headings.forEach((heading) => {
        if (!heading.querySelector('.edit-section')) {
            const span = document.createElement('span');
            span.className = 'edit-section';
            span.innerHTML = '[<a href="#" onclick="return false;">edit</a>]';
            heading.appendChild(span);
        }
    });
}

let tocDebounceTimer = null;
function debouncedTOCUpdate() {
    clearTimeout(tocDebounceTimer);
    tocDebounceTimer = setTimeout(() => {
        generateTOC();
        addEditSectionLinks();
    }, 500);
}
