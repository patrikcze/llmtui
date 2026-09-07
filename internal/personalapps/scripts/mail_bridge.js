// mail_bridge.js — fixed, reviewed JXA bridge for Apple Mail automation.
//
// This script is embedded into the llmtui binary via go:embed and invoked
// exactly as-is through osascript's -e argument; it is never modified,
// templated or concatenated with request data. The only variable input is
// one bounded JSON document read from stdin; the only output is one bounded
// JSON document written to stdout via the script's own top-level return
// value (osascript prints a returned JXA string's raw characters, which
// this script always makes a JSON.stringify result).
//
// Every operation is read-only. This script has no code path that creates,
// moves, deletes or sends anything — mutation support is separately scoped
// future work (see the plan's Slice 6) and would be a different, separately
// reviewed script, not an extension of this one.
//
// Wire version: keep bridgeVersion in mail_bridge.go equal to the version
// literals below. A mismatch must be a hard error on the Go side, never a
// best-effort fallback.

ObjC.import('Foundation');

function readStdin() {
  var data = $.NSFileHandle.fileHandleWithStandardInput.readDataToEndOfFile;
  return $.NSString.alloc.initWithDataEncoding(data, $.NSUTF8StringEncoding).js;
}

// disambiguate assigns unique path segments to a list of sibling mailbox
// names, appending " (2)", " (3)", ... to repeats. Mail's own scripting
// dictionary allows two sibling mailboxes with the identical display name
// (observed empirically on a live Gmail account during the Slice 0 spike),
// and mailboxes have no stable id property at all — so a deterministic,
// order-based disambiguation is the only way to give each sibling a unique,
// re-resolvable path segment. It depends on Mail reporting siblings in a
// stable order within one process lifetime, which is what every call here
// observes but is not an Apple-documented guarantee.
function disambiguate(names) {
  var seen = {};
  return names.map(function (n) {
    seen[n] = (seen[n] || 0) + 1;
    return seen[n] === 1 ? n : n + ' (' + seen[n] + ')';
  });
}

function safeChildren(mailboxObj) {
  try {
    return mailboxObj.mailboxes();
  } catch (e) {
    return [];
  }
}

// navigateToMailbox walks from an account's root mailboxes down through
// pathSegments, re-deriving the same disambiguated names navigateToMailbox
// and mailboxListing agree on at every level, and returns the mailbox
// object at the end of the path, or null if any segment cannot be matched
// (a rename, move or deletion since the path was issued).
function navigateToMailbox(accountObj, pathSegments) {
  var siblings = accountObj.mailboxes();
  var current = null;
  for (var i = 0; i < pathSegments.length; i++) {
    var names = siblings.map(function (mb) {
      return mb.name();
    });
    var segs = disambiguate(names);
    var idx = segs.indexOf(pathSegments[i]);
    if (idx === -1) return null;
    current = siblings[idx];
    siblings = safeChildren(current);
  }
  return current;
}

function findAccount(accounts, accountId) {
  for (var i = 0; i < accounts.length; i++) {
    var id;
    try {
      id = accounts[i].id();
    } catch (e) {
      continue;
    }
    if (id === accountId) return accounts[i];
  }
  return null;
}

function mailboxListing(children) {
  var names = children.map(function (mb) {
    return mb.name();
  });
  var segs = disambiguate(names);
  return children.map(function (mb, i) {
    var unread = 0,
      total = 0,
      hasChildren = false;
    try {
      unread = mb.unreadCount();
    } catch (e) {
      /* metadata best-effort */
    }
    try {
      total = mb.messages().length;
    } catch (e) {
      /* metadata best-effort; large mailboxes are not yet benchmarked */
    }
    try {
      hasChildren = safeChildren(mb).length > 0;
    } catch (e) {
      /* metadata best-effort */
    }
    return { name: mb.name(), path_segment: segs[i], has_children: hasChildren, unread: unread, total: total };
  });
}

function errorResponse(op, code, message) {
  return { version: 1, op: op || '', error: { code: code, message: String(message) } };
}

function isoDate(d) {
  try {
    return d.toISOString();
  } catch (e) {
    return null;
  }
}

// messageMeta reads only bounded metadata properties — never content — so
// search stays a metadata-first scan as the architecture requires.
function messageMeta(msg, accountId, path) {
  var m = { account_id: accountId, path: path };
  try {
    m.native_id = String(msg.id());
  } catch (e) {
    m.native_id = '';
  }
  try {
    m.message_id = msg.messageId() || '';
  } catch (e) {
    m.message_id = '';
  }
  try {
    m.subject = msg.subject() || '';
  } catch (e) {
    m.subject = '';
  }
  try {
    m.from = msg.sender() || '';
  } catch (e) {
    m.from = '';
  }
  try {
    m.to = msg.toRecipients().map(function (r) {
      try {
        return r.address();
      } catch (e2) {
        return '';
      }
    });
  } catch (e) {
    m.to = [];
  }
  try {
    m.cc = msg.ccRecipients().map(function (r) {
      try {
        return r.address();
      } catch (e2) {
        return '';
      }
    });
  } catch (e) {
    m.cc = [];
  }
  try {
    m.received = isoDate(msg.dateReceived());
  } catch (e) {
    m.received = null;
  }
  try {
    // Mail's "read status" is true once a message has been read; the
    // model-facing field is the inverse, "unread".
    m.unread = !msg.readStatus();
  } catch (e) {
    m.unread = false;
  }
  try {
    m.flagged = !!msg.flaggedStatus();
  } catch (e) {
    m.flagged = false;
  }
  try {
    m.has_attachments = msg.mailAttachments().length > 0;
  } catch (e) {
    m.has_attachments = false;
  }
  return m;
}

// messageBody reads content only for explicitly selected messages, never
// during search, and reports unavailable content as unavailable rather than
// an empty body that would read like success.
function messageBody(msg, maxChars) {
  var out = { body_available: false, body_truncated: false, unavailable: '' };
  var content;
  try {
    content = msg.content();
  } catch (e) {
    content = null;
  }
  if (content === null || content === undefined) {
    out.unavailable = 'content_unavailable';
    return out;
  }
  content = String(content);
  out.body_available = true;
  if (maxChars > 0 && content.length > maxChars) {
    out.body = content.substring(0, maxChars);
    out.body_truncated = true;
  } else {
    out.body = content;
  }
  var atts = [];
  try {
    var raw = msg.mailAttachments();
    for (var i = 0; i < raw.length; i++) {
      var a = { name: '', size: 0 };
      try {
        a.name = raw[i].name() || '';
      } catch (e) {
        /* best-effort */
      }
      try {
        a.size = raw[i].fileSize() || 0;
      } catch (e) {
        /* best-effort */
      }
      atts.push(a);
    }
  } catch (e) {
    /* no attachments accessible */
  }
  out.attachments = atts;
  return out;
}

function opAccounts(Mail) {
  var accounts = Mail.accounts();
  var list = accounts
    .map(function (a) {
      var id, name;
      try {
        id = a.id();
      } catch (e) {
        id = '';
      }
      try {
        name = a.name();
      } catch (e) {
        name = '';
      }
      return { id: id, name: name };
    })
    .filter(function (a) {
      return a.id !== '';
    });
  return { version: 1, op: 'accounts', accounts: list };
}

function opMailboxes(Mail, req) {
  var accounts = Mail.accounts();
  var account = findAccount(accounts, req.account_id);
  if (!account) return errorResponse('mailboxes', 'not_found', 'account not found');
  var children;
  if (req.parent_path && req.parent_path.length > 0) {
    var parent = navigateToMailbox(account, req.parent_path);
    if (!parent) return errorResponse('mailboxes', 'not_found', 'mailbox not found');
    children = safeChildren(parent);
  } else {
    children = account.mailboxes();
  }
  return { version: 1, op: 'mailboxes', mailboxes: mailboxListing(children) };
}

function resolveScope(Mail, scope) {
  var accounts = Mail.accounts();
  var account = findAccount(accounts, scope.account_id);
  if (!account) return null;
  var mailbox = navigateToMailbox(account, scope.path);
  if (!mailbox) return null;
  return { account: account, mailbox: mailbox };
}

// opSearch runs a bounded, metadata-only scan of the requested mailboxes,
// newest message first, and reports honest coverage: scanned is how many
// candidates were actually examined, complete is true only when nothing
// was left unexamined, and resume/reason describe exactly where and why a
// continuation is possible. It never fetches message content.
function opSearch(Mail, req) {
  var s = req.search;
  var resolvedScopes = [];
  for (var i = 0; i < s.scopes.length; i++) {
    var r = resolveScope(Mail, s.scopes[i]);
    if (!r) return errorResponse('search', 'not_found', 'a search scope could not be resolved');
    resolvedScopes.push(r);
  }

  var after = s.received_after ? new Date(s.received_after) : null;
  var before = s.received_before ? new Date(s.received_before) : null;
  var fromNeedle = (s.from || '').toLowerCase();
  var subjectNeedle = (s.subject || '').toLowerCase();
  var limit = s.limit > 0 ? s.limit : 25;
  var maxCandidates = s.max_candidates > 0 ? s.max_candidates : 1000;

  var startScope = 0,
    startIdx = -1;
  if (s.resume) {
    startScope = s.resume.scope_index || 0;
    startIdx = typeof s.resume.message_index === 'number' ? s.resume.message_index : -1;
  }

  var results = [];
  var scanned = 0;
  var reason = '';
  var resume = null;

  outer: for (var si = startScope; si < resolvedScopes.length; si++) {
    var scope = resolvedScopes[si];
    var path = s.scopes[si].path;
    var msgs;
    try {
      msgs = scope.mailbox.messages();
    } catch (e) {
      reason = 'mailbox_unavailable';
      break outer;
    }
    var len = msgs.length;
    var idx = si === startScope && startIdx >= 0 ? startIdx : len - 1;
    for (; idx >= 0; idx--) {
      if (scanned >= maxCandidates) {
        reason = 'scan_limit';
        resume = { scope_index: si, message_index: idx };
        break outer;
      }
      scanned++;
      var meta = messageMeta(msgs[idx], s.scopes[si].account_id, path);
      if (!meta.received) continue;
      var received = new Date(meta.received);
      if (after && received < after) continue;
      if (before && received >= before) continue;
      if ((s.unread === true || s.unread === false) && meta.unread !== s.unread) continue;
      if ((s.flagged === true || s.flagged === false) && meta.flagged !== s.flagged) continue;
      if (fromNeedle && meta.from.toLowerCase().indexOf(fromNeedle) === -1) continue;
      if (subjectNeedle && meta.subject.toLowerCase().indexOf(subjectNeedle) === -1) continue;
      results.push(meta);
      if (results.length >= limit) {
        if (idx > 0) {
          reason = 'page_limit';
          resume = { scope_index: si, message_index: idx - 1 };
        } else if (si + 1 < resolvedScopes.length) {
          reason = 'page_limit';
          resume = { scope_index: si + 1, message_index: -1 };
        }
        break outer;
      }
    }
  }

  results.sort(function (a, b) {
    if (a.received === b.received) {
      return parseInt(a.native_id, 10) < parseInt(b.native_id, 10) ? 1 : -1;
    }
    return a.received < b.received ? 1 : -1;
  });
  if (!s.newest) results.reverse();

  var page = { messages: results, scanned: scanned, complete: resume === null && reason === '' };
  if (reason) page.reason = reason;
  if (resume) page.resume = resume;
  return { version: 1, op: 'search', page: page };
}

// opMessages reads bounded content for explicitly selected messages only,
// looked up by their recorded (account, mailbox path, native id) — never by
// array position, and never by RFC Message-ID alone, per the architecture's
// identity rules. A message that has moved, or an id that no longer exists
// at its recorded location, is reported not_found rather than guessed at.
function opMessages(Mail, req) {
  var m = req.messages;
  var accounts = Mail.accounts();
  var maxChars = m.max_body_chars > 0 ? m.max_body_chars : 8000;
  var out = [];
  for (var i = 0; i < m.refs.length; i++) {
    var ref = m.refs[i];
    var account = findAccount(accounts, ref.account_id);
    if (!account) return errorResponse('messages', 'not_found', 'account not found for a selected message');
    var mailbox = navigateToMailbox(account, ref.path);
    if (!mailbox) return errorResponse('messages', 'not_found', 'mailbox not found for a selected message');
    var found;
    try {
      found = mailbox.messages.whose({ id: parseInt(ref.native_id, 10) })();
    } catch (e) {
      found = [];
    }
    if (!found || found.length === 0) {
      return errorResponse('messages', 'not_found', 'message not found at its recorded location');
    }
    var msg = found[0];
    var meta = messageMeta(msg, ref.account_id, ref.path);
    var body = messageBody(msg, maxChars);
    var full = {};
    for (var k in meta) full[k] = meta[k];
    for (var k2 in body) full[k2] = body[k2];
    out.push(full);
  }
  return { version: 1, op: 'messages', messages: out };
}

function handle(req) {
  if (!req || req.version !== 1) {
    return errorResponse(req && req.op, 'invalid_request', 'unsupported request version');
  }
  var Mail;
  try {
    Mail = Application('Mail');
  } catch (e) {
    return errorResponse(req.op, 'app_unavailable', 'Mail is not available');
  }
  try {
    switch (req.op) {
      case 'accounts':
        return opAccounts(Mail);
      case 'mailboxes':
        return opMailboxes(Mail, req);
      case 'search':
        return opSearch(Mail, req);
      case 'messages':
        return opMessages(Mail, req);
      default:
        return errorResponse(req.op, 'invalid_request', 'unknown operation');
    }
  } catch (e) {
    var code = 'internal';
    var msg = String(e);
    var low = msg.toLowerCase();
    if (low.indexOf('-1743') !== -1 || low.indexOf('not allowed') !== -1 || low.indexOf('not authorized') !== -1) {
      code = 'permission_denied';
    }
    return errorResponse(req.op, code, msg);
  }
}

(function () {
  var input;
  try {
    input = JSON.parse(readStdin());
  } catch (e) {
    return JSON.stringify(errorResponse(null, 'invalid_request', 'malformed request JSON'));
  }
  return JSON.stringify(handle(input));
})();
