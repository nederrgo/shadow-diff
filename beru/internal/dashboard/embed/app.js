(function () {
  const runSelect = document.getElementById('run-select');
  if (runSelect) {
    runSelect.addEventListener('change', function () {
      const run = runSelect.value;
      const params = new URLSearchParams(window.location.search);
      params.set('run', run);
      window.location.search = params.toString();
    });
  }

  document.querySelectorAll('.filter-tab').forEach(function (btn) {
    btn.addEventListener('click', function () {
      const filter = btn.getAttribute('data-filter');
      const run = runSelect ? runSelect.value : '';
      const params = new URLSearchParams();
      if (run) params.set('run', run);
      if (filter && filter !== 'all') params.set('filter', filter);
      window.location.search = params.toString();
    });
  });

  document.querySelectorAll('.ignore-path').forEach(function (btn) {
    btn.addEventListener('click', async function () {
      const path = btn.getAttribute('data-path');
      const shadowTest = btn.getAttribute('data-shadow-test');
      btn.disabled = true;
      try {
        const res = await fetch('/api/v1/noise/filters', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ shadow_test_name: shadowTest, path: path }),
        });
        if (!res.ok) throw new Error('save failed');
        btn.textContent = 'Ignored';
        btn.classList.add('bg-green-50', 'border-green-300');
      } catch (e) {
        btn.disabled = false;
        btn.textContent = 'Retry';
      }
    });
  });
})();
