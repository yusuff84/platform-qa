(function () {
  'use strict';

  var KEY = 'e2e-theme';
  var root = document.documentElement;

  function currentTheme() {
    return root.classList.contains('light') ? 'light' : 'dark';
  }

  function applyIcon() {
    var light = currentTheme() === 'light';
    var icon = document.getElementById('themeIcon');
    if (icon) icon.className = 'fa-solid ' + (light ? 'fa-sun' : 'fa-moon');
    root.style.colorScheme = light ? 'light' : 'dark';
  }

  function toggleTheme() {
    var next = currentTheme() === 'light' ? 'dark' : 'light';
    root.classList.remove('light', 'dark');
    root.classList.add(next);
    try { localStorage.setItem(KEY, next); } catch (e) {}
    applyIcon();
  }

  window.toggleTheme = toggleTheme;

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', applyIcon);
  } else {
    applyIcon();
  }
})();
