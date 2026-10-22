/* Duo site: progressive enhancement only. Every page reads and navigates without it. */
(function () {
  'use strict';

  var root = document.documentElement;
  root.classList.remove('no-js');
  root.classList.add('js');

  function fallbackCopy(text) {
    return new Promise(function (resolve, reject) {
      var area = document.createElement('textarea');
      area.value = text;
      area.setAttribute('readonly', '');
      area.style.position = 'fixed';
      area.style.top = '0';
      area.style.left = '-9999px';
      document.body.appendChild(area);
      area.select();
      var ok = false;
      try {
        ok = document.execCommand('copy');
      } catch (err) {
        ok = false;
      }
      document.body.removeChild(area);
      if (ok) resolve();
      else reject(new Error('copy failed'));
    });
  }

  function copyText(text) {
    if (navigator.clipboard && window.isSecureContext) {
      return navigator.clipboard.writeText(text).catch(function () {
        return fallbackCopy(text);
      });
    }
    return fallbackCopy(text);
  }

  function wireCopyButtons() {
    var chinese = root.lang && root.lang.indexOf('zh') === 0;
    var copiedLabel = chinese ? '已复制' : 'Copied';
    var failedLabel = chinese ? '复制失败' : 'Copy failed';
    var buttons = document.querySelectorAll('[data-copy]');

    Array.prototype.forEach.call(buttons, function (button) {
      var selector = button.getAttribute('data-copy');
      var target = selector && document.querySelector(selector);
      var label = button.querySelector('.copy-label');
      var status = button.parentNode && button.parentNode.querySelector('.copy-status');
      if (!target) return;

      var timer = null;
      var original = label ? label.textContent : '';

      button.addEventListener('click', function () {
        var text = target.textContent.replace(/^\s*\$\s+/, '').trim();
        if (!text) return;

        copyText(text).then(function () {
          button.setAttribute('data-copied', 'true');
          if (label) label.textContent = copiedLabel;
          if (status) status.textContent = copiedLabel;
          if (timer) window.clearTimeout(timer);
          timer = window.setTimeout(function () {
            button.removeAttribute('data-copied');
            if (label) label.textContent = original;
            if (status) status.textContent = '';
          }, 1600);
        }).catch(function () {
          if (status) status.textContent = failedLabel;
          if (target.focus) target.focus();
        });
      });
    });
  }

  function wireMobileNav() {
    var toggle = document.querySelector('[data-nav-toggle]');
    var menu = document.getElementById('nav-menu');
    if (!toggle || !menu) return;

    var desktop = window.matchMedia('(min-width: 861px)');

    function setOpen(open) {
      menu.classList.toggle('is-open', open);
      toggle.classList.toggle('is-open', open);
      toggle.setAttribute('aria-expanded', open ? 'true' : 'false');
      if (open) menu.removeAttribute('hidden');
      else if (!desktop.matches) menu.setAttribute('hidden', '');
      else menu.removeAttribute('hidden');
    }

    function syncLayout() {
      if (desktop.matches) {
        menu.classList.remove('is-open');
        toggle.classList.remove('is-open');
        toggle.setAttribute('aria-expanded', 'false');
        menu.removeAttribute('hidden');
      } else if (!menu.classList.contains('is-open')) {
        menu.setAttribute('hidden', '');
      }
    }

    toggle.addEventListener('click', function () {
      setOpen(!menu.classList.contains('is-open'));
    });

    menu.addEventListener('click', function (event) {
      if (event.target.closest && event.target.closest('a')) setOpen(false);
    });

    document.addEventListener('keydown', function (event) {
      if (event.key === 'Escape' && menu.classList.contains('is-open')) {
        setOpen(false);
        toggle.focus();
      }
    });

    document.addEventListener('click', function (event) {
      if (!menu.classList.contains('is-open')) return;
      if (menu.contains(event.target) || toggle.contains(event.target)) return;
      setOpen(false);
    });

    if (desktop.addEventListener) desktop.addEventListener('change', syncLayout);
    else desktop.addListener(syncLayout);

    syncLayout();
  }

  function wireReveal() {
    var items = document.querySelectorAll('.reveal');
    if (!items.length) return;

    var reduced = window.matchMedia &&
      window.matchMedia('(prefers-reduced-motion: reduce)').matches;

    if (reduced || !('IntersectionObserver' in window)) {
      Array.prototype.forEach.call(items, function (item) {
        item.classList.add('is-visible');
      });
      return;
    }

    var observer = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        entry.target.classList.add('is-visible');
        observer.unobserve(entry.target);
      });
    }, { rootMargin: '0px 0px -10% 0px', threshold: 0.08 });

    Array.prototype.forEach.call(items, function (item) {
      observer.observe(item);
    });
  }

  function init() {
    wireCopyButtons();
    wireMobileNav();
    wireReveal();
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', init);
  } else {
    init();
  }
})();
