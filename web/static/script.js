// DOM Elements
const topicInput = document.getElementById('topicInput');
const startButton = document.getElementById('startButton');
const reviseButton = document.getElementById('reviseButton');
const loadingIndicator = document.getElementById('loading');
const mainContent = document.getElementById('mainContent');
const articleEl = document.getElementById('article');
const topicEl = document.getElementById('topic');
const critiqueEl = document.getElementById('critique');
const historyContainer = document.getElementById('historyContainer');
const historyEl = document.getElementById('history');

// State
let articleState = null;

// --- Event Listeners ---
startButton.addEventListener('click', handleStart);
reviseButton.addEventListener('click', handleRevision);

// --- Functions ---

/**
 * Reads an SSE stream from a fetch response and dispatches token events.
 * Returns the final ArticleState from the "done" event.
 */
async function streamSSE(response, onArticleToken, onCritiqueToken) {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = '';
    let currentEvent = '';
    let result = null;

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
                if (currentEvent === 'article_token') {
                    onArticleToken(JSON.parse(raw));
                } else if (currentEvent === 'critique_token') {
                    onCritiqueToken(JSON.parse(raw));
                } else if (currentEvent === 'done') {
                    result = JSON.parse(JSON.parse(raw));
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
    // Show content area early so tokens are visible
    mainContent.classList.remove('hidden');
    topicEl.textContent = topic;
    articleEl.innerHTML = '';
    critiqueEl.innerHTML = '';

    let articleText = '';
    let critiqueText = '';

    try {
        const response = await fetch('/api/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ topic }),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await streamSSE(
            response,
            (token) => {
                articleText += token;
                articleEl.innerHTML = marked.parse(articleText);
                debouncedTOCUpdate();
            },
            (token) => {
                critiqueText += token;
                critiqueEl.innerHTML = marked.parse(critiqueText);
            },
        );

        // Final render with complete state
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
    articleEl.innerHTML = '';
    critiqueEl.innerHTML = '';

    let articleText = '';
    let critiqueText = '';

    try {
        const response = await fetch('/api/continue', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(articleState),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await streamSSE(
            response,
            (token) => {
                articleText += token;
                articleEl.innerHTML = marked.parse(articleText);
                debouncedTOCUpdate();
            },
            (token) => {
                critiqueText += token;
                critiqueEl.innerHTML = marked.parse(critiqueText);
            },
        );

        // Final render with complete state
        render();

    } catch (error) {
        alert(`Failed to revise article: ${error.message}`);
    } finally {
        setLoading(false);
    }
}

/**
 * Updates the UI based on the current articleState.
 */
function render() {
    if (!articleState) return;

    // Populate the main content
    topicEl.textContent = articleState.topic;
    articleEl.innerHTML = marked.parse(articleState.current_article);
    critiqueEl.innerHTML = marked.parse(articleState.last_critique);

    // Update and show revision history if it exists
    if (articleState.revision_history && articleState.revision_history.length > 0) {
        historyEl.innerHTML = articleState.revision_history
            .map((crit, index) => `<h3>Critique from Round ${index + 1}</h3><p>${crit}</p>`)
            .join('<hr>');
        historyContainer.classList.remove('hidden');
    }

    // Show the main content area
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

// --- Critique Toggle ---

const critiqueToggle = document.getElementById('critiqueToggle');
const critiqueBody = document.getElementById('critiqueBody');

critiqueToggle.addEventListener('click', () => {
    critiqueToggle.classList.toggle('collapsed');
    critiqueBody.classList.toggle('collapsed');
});
