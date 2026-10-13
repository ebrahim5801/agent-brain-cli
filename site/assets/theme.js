// Applies the saved light/dark choice before the page renders. Loaded
// synchronously in <head> (not deferred) so there is no flash of the wrong
// theme; kept out of the HTML so the site runs under script-src 'self'.
(function(){
  try {
    var t = localStorage.getItem('agentbrain-theme');
    if (t === 'dark' || t === 'light') document.documentElement.setAttribute('data-theme', t);
  } catch (e) {}
  document.documentElement.classList.add('js');
})();
