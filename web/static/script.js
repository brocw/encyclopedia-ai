// DOM Elements
const topicInput = document.getElementById('topicInput');
const startButton = document.getElementById('startButton');
const reviseButton = document.getElementById('reviseButton');
const loadingIndicator = document.getElementById('loading');
const mainContent = document.getElementById('mainContent');
const articleEl = document.getElementById('article');
const topicEl = document.getElementById('topic');
const factcheckEl = document.getElementById('factcheck');
const infoboxEl = document.getElementById('infobox');
const seeAlsoEl = document.getElementById('seeAlso');
const referencesEl = document.getElementById('references');
const historyContainer = document.getElementById('historyContainer');
const historyEl = document.getElementById('history');
const categoriesEl = document.getElementById('articleCategories');

// State
let articleState = null;
let agentFirstToken = {};

// --- Event Listeners ---
startButton.addEventListener('click', handleStart);
reviseButton.addEventListener('click', handleRevision);

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
        factcheck_token: callbacks.onFactCheckToken,
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
        // Keep the last potentially incomplete line in the buffer
        buffer = lines.pop();

        for (const line of lines) {
            if (line.startsWith('event: ')) {
                currentEvent = line.slice(7);
            } else if (line.startsWith('data: ')) {
                const raw = line.slice(6);
                const handler = eventMap[currentEvent];
                if (handler) {
                    handler(JSON.parse(raw));
                } else if (currentEvent === 'article_done') {
                    setAgentStatus('agent-article', 'done');
                    ['agent-factcheck', 'agent-references', 'agent-infobox',
                     'agent-seealso', 'agent-categories'].forEach(id => setAgentStatus(id, 'active'));
                } else if (currentEvent === 'done') {
                    result = JSON.parse(JSON.parse(raw));
                    ['agent-article', 'agent-factcheck', 'agent-references',
                     'agent-infobox', 'agent-seealso', 'agent-categories'].forEach(id => setAgentStatus(id, 'done'));
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
 * Handles the initial "Start" button click.
 */
async function handleStart() {
    const topic = topicInput.value.trim();
    if (!topic) {
        alert('Please enter a topic.');
        return;
    }

    setLoading(true);
    resetAgentProgress();
    setAgentStatus('agent-article', 'active');
    mainContent.classList.remove('hidden');
    topicEl.textContent = topic;
    clearContent();

    let articleText = '';
    let factcheckText = '';
    let referencesText = '';
    let infoboxText = '';
    let seeAlsoText = '';
    let categoryText = '';

    try {
        const response = await fetch('/api/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ topic }),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await streamSSE(response, {
            onArticleToken(token) {
                articleText += token;
                articleEl.innerHTML = marked.parse(articleText);
                debouncedTOCUpdate();
            },
            onFactCheckToken(token) {
                factcheckText += token;
                factcheckEl.innerHTML = marked.parse(factcheckText);
            },
            onReferencesToken(token) {
                referencesText += token;
                referencesEl.innerHTML = marked.parse(referencesText);
                document.getElementById('references-section').classList.remove('hidden');
            },
            onInfoboxToken(token) {
                infoboxText += token;
                infoboxEl.innerHTML = marked.parse(infoboxText);
                infoboxEl.classList.remove('hidden');
            },
            onSeeAlsoToken(token) {
                seeAlsoText += token;
                seeAlsoEl.innerHTML = marked.parse(seeAlsoText);
                document.getElementById('seealso-section').classList.remove('hidden');
            },
            onCategoryToken(token) {
                categoryText += token;
                categoriesEl.textContent = categoryText;
            },
        });

        render();

    } catch (error) {
        alert(`Failed to start article: ${error.message}`);
    } finally {
        setLoading(false);
    }
}

/**
 * Handles the "Revise Article" button click.
 */
async function handleRevision() {
    if (!articleState) {
        alert('No article to revise.');
        return;
    }

    setLoading(true);
    resetAgentProgress();
    setAgentStatus('agent-article', 'active');
    clearContent();

    let articleText = '';
    let factcheckText = '';
    let referencesText = '';
    let infoboxText = '';
    let seeAlsoText = '';
    let categoryText = '';

    try {
        const response = await fetch('/api/continue', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(articleState),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await streamSSE(response, {
            onArticleToken(token) {
                articleText += token;
                articleEl.innerHTML = marked.parse(articleText);
                debouncedTOCUpdate();
            },
            onFactCheckToken(token) {
                factcheckText += token;
                factcheckEl.innerHTML = marked.parse(factcheckText);
            },
            onReferencesToken(token) {
                referencesText += token;
                referencesEl.innerHTML = marked.parse(referencesText);
                document.getElementById('references-section').classList.remove('hidden');
            },
            onInfoboxToken(token) {
                infoboxText += token;
                infoboxEl.innerHTML = marked.parse(infoboxText);
                infoboxEl.classList.remove('hidden');
            },
            onSeeAlsoToken(token) {
                seeAlsoText += token;
                seeAlsoEl.innerHTML = marked.parse(seeAlsoText);
                document.getElementById('seealso-section').classList.remove('hidden');
            },
            onCategoryToken(token) {
                categoryText += token;
                categoriesEl.textContent = categoryText;
            },
        });

        render();

    } catch (error) {
        alert(`Failed to revise article: ${error.message}`);
    } finally {
        setLoading(false);
    }
}

/**
 * Clears all content areas for a fresh render.
 */
function clearContent() {
    articleEl.innerHTML = '';
    factcheckEl.innerHTML = '';
    infoboxEl.innerHTML = '';
    infoboxEl.classList.add('hidden');
    seeAlsoEl.innerHTML = '';
    document.getElementById('seealso-section').classList.add('hidden');
    referencesEl.innerHTML = '';
    document.getElementById('references-section').classList.add('hidden');
    categoriesEl.textContent = '';
}

/**
 * Updates the UI based on the current articleState.
 */
function render() {
    if (!articleState) return;

    topicEl.textContent = articleState.topic;
    articleEl.innerHTML = marked.parse(articleState.current_article);

    if (articleState.fact_check) {
        factcheckEl.innerHTML = marked.parse(articleState.fact_check);
    }

    if (articleState.infobox) {
        infoboxEl.innerHTML = marked.parse(articleState.infobox);
        infoboxEl.classList.remove('hidden');
    }

    if (articleState.see_also) {
        seeAlsoEl.innerHTML = marked.parse(articleState.see_also);
        document.getElementById('seealso-section').classList.remove('hidden');
    }

    if (articleState.references) {
        referencesEl.innerHTML = marked.parse(articleState.references);
        document.getElementById('references-section').classList.remove('hidden');
    }

    if (articleState.categories) {
        categoriesEl.textContent = articleState.categories;
    }

    // Update and show revision history if it exists
    if (articleState.revision_history && articleState.revision_history.length > 0) {
        historyEl.innerHTML = articleState.revision_history
            .map((fc, index) => `<h3>Fact-Check from Round ${index + 1}</h3>${marked.parse(fc)}`)
            .join('<hr>');
        historyContainer.classList.remove('hidden');
    }

    mainContent.classList.remove('hidden');

    generateTOC();
    addEditSectionLinks();
}

/**
 * Sets the status of an agent progress row.
 */
function setAgentStatus(agentId, status) {
    const li = document.getElementById(agentId);
    if (!li) return;
    const span = li.querySelector('.agent-status');
    span.className = 'agent-status ' + status;
    li.classList.remove('is-active', 'is-done');
    if (status === 'active') li.classList.add('is-active');
    if (status === 'done') li.classList.add('is-done');
}

/**
 * Resets all agent progress rows to pending.
 */
function resetAgentProgress() {
    agentFirstToken = {};
    const agents = ['agent-article', 'agent-factcheck', 'agent-references',
                     'agent-infobox', 'agent-seealso', 'agent-categories'];
    agents.forEach(id => setAgentStatus(id, 'pending'));
}

/**
 * Manages the visibility of the loading indicator and disables buttons.
 */
function setLoading(isLoading) {
    if (isLoading) {
        loadingIndicator.classList.remove('hidden');
        startButton.disabled = true;
        reviseButton.disabled = true;
    } else {
        loadingIndicator.classList.add('hidden');
        startButton.disabled = false;
        reviseButton.disabled = false;
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

// --- Fact-Check Toggle ---

const factcheckToggle = document.getElementById('factcheckToggle');
const factcheckBody = document.getElementById('factcheckBody');

factcheckToggle.addEventListener('click', () => {
    factcheckToggle.classList.toggle('collapsed');
    factcheckBody.classList.toggle('collapsed');
});
