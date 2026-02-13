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
 * Handles the initial "Start" button click.
 */
async function handleStart() {
    const topic = topicInput.value.trim();
    if (!topic) {
        alert('Please enter a topic.');
        return;
    }

    setLoading(true);

    try {
        const response = await fetch('/api/start', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ topic }),
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await response.json();
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

    try {
        const response = await fetch('/api/continue', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(articleState), // Send the full current state
        });

        if (!response.ok) {
            throw new Error(`Server error: ${response.statusText}`);
        }

        articleState = await response.json();
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
