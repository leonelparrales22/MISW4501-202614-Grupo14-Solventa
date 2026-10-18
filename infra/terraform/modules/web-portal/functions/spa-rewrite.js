// Viewer-request function of the web portal (T5, decision D-1), runtime
// cloudfront-js-2.0. A path whose last segment has no dot is a route of the
// single-page application (/embebido/oferta/<id>, /originacion/bandeja, /), so
// CloudFront serves /index.html and the SPA draws the route. Files (bundles,
// config.json, i18n dictionaries, favicon) pass through unchanged, so a missing
// file still answers the 403 of S3 instead of the application.
function handler(event) {
  var request = event.request;
  var uri = request.uri;
  var lastSegment = uri.substring(uri.lastIndexOf('/') + 1);

  if (lastSegment.indexOf('.') === -1) {
    request.uri = '/index.html';
  }

  return request;
}
