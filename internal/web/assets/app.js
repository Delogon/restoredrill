// restoredrill dashboard — hand-written, no framework, no inline scripts.
// Reading works without JavaScript; this only adds live updates.
(function () {
  "use strict";

  function pad(n) { return n < 10 ? "0" + n : "" + n; }

  function fmtLine(e) {
    var d = new Date(e.ts);
    var ts = d.getFullYear() + "-" + pad(d.getMonth() + 1) + "-" + pad(d.getDate()) +
      " " + pad(d.getHours()) + ":" + pad(d.getMinutes()) + ":" + pad(d.getSeconds());
    return ts + " " + e.level + " " + e.step + " " + e.message + "\n";
  }

  // Live log streaming on the run and live pages.
  var log = document.getElementById("log");
  if (log && log.dataset.events && log.dataset.running === "true" && typeof EventSource !== "undefined") {
    var es = new EventSource(log.dataset.events);
    es.addEventListener("log", function (ev) {
      try {
        log.textContent += fmtLine(JSON.parse(ev.data));
        log.scrollTop = log.scrollHeight;
      } catch (err) { /* ignore malformed events */ }
    });
    es.addEventListener("done", function () {
      es.close();
      // Reload to render the final status, checks and timeline.
      window.location.reload();
    });
    es.onerror = function () { /* EventSource reconnects automatically */ };
  }

  // Overview: refresh the table when a run starts or finishes.
  var table = document.getElementById("overview-table");
  if (table && table.dataset.sse && typeof EventSource !== "undefined") {
    var src = new EventSource(table.dataset.sse);
    src.addEventListener("changed", function () {
      fetch(table.dataset.partial, { headers: { "Accept": "text/html" } })
        .then(function (resp) { return resp.ok ? resp.text() : ""; })
        .then(function (html) {
          if (!html) { return; }
          var tbody = table.querySelector("tbody");
          if (tbody) { tbody.innerHTML = html; }
        })
        .catch(function () { /* keep the last rendered table */ });
    });
  }
})();