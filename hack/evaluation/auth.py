#!/usr/bin/env python3
"""Authentication transport safeguards; API calls remain the authentication authority."""
import urllib.request


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None
