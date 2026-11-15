"use strict";

const { parse } = require("../../.lint/node_modules/@textlint/markdown-to-ast");
const { posix } = require("node:path");
const { readdirSync, readFileSync } = require("node:fs");

const WIKI_URL = "https://github.com/markhuangai/dense-mem/wiki";

function isWikiFile(name) {
  return /^[^/.][^/]*\.md$/.test(name) || /^(assets|images)\//.test(name);
}

function readDirectory(directory, prefix = "") {
  const files = new Map();
  for (const entry of readdirSync(directory, { withFileTypes: true })) {
    const name = prefix + entry.name;
    if (entry.name.startsWith(".")) continue;
    if (entry.isDirectory() && (name === "assets" || name === "images" || prefix)) {
      for (const [key, value] of readDirectory(`${directory}/${entry.name}`, `${name}/`)) files.set(key, value);
    } else if (entry.isFile() && isWikiFile(name)) {
      files.set(name, readFileSync(`${directory}/${entry.name}`));
    } else {
      throw new Error(`unsupported wiki file: ${name}`);
    }
  }
  return files;
}

function walk(node, visit) {
  visit(node);
  for (const child of node.children || []) walk(child, visit);
}

function text(node) {
  if (node.type === "Html") return "";
  return node.children ? node.children.map(text).join("") : node.value || node.alt || "";
}

async function validateWiki(files) {
  if (!files.has("Home.md") || !files.has("_Sidebar.md")) throw new Error("wiki requires Home.md and _Sidebar.md");
  const { default: Slugger } = await import("../../.lint/node_modules/github-slugger/index.js");
  const pages = new Map();
  for (const [name, bytes] of files) {
    if (!name.endsWith(".md") || name.includes("/")) continue;
    const headings = new Set();
    const links = [];
    const references = new Map();
    const unresolved = [];
    const slugger = new Slugger();
    walk(parse(bytes.toString("utf8")), (node) => {
      if (node.type === "Header") headings.add(slugger.slug(text(node)));
      if (node.type === "Link" || node.type === "Image") links.push(node.url);
      if (node.type === "Definition") references.set(node.identifier.toLowerCase(), node.url);
      if (node.type === "LinkReference" || node.type === "ImageReference") unresolved.push(node.identifier.toLowerCase());
      if (node.type === "Html") {
        for (const match of node.value.matchAll(/\b(?:id|name)=["']([^"']+)["']/g)) headings.add(match[1]);
        for (const match of node.value.matchAll(/\b(?:href|src)=["']([^"']+)["']/g)) links.push(match[1]);
      }
    });
    for (const reference of unresolved) {
      if (!references.has(reference)) throw new Error(`${name}: undefined link reference ${reference}`);
      links.push(references.get(reference));
    }
    pages.set(name, { headings, links });
  }
  for (const [name, { links }] of pages) {
    for (let target of links) {
      if (target === WIKI_URL || target === `${WIKI_URL}/`) target = "Home";
      else if (target.startsWith(`${WIKI_URL}/`)) target = target.slice(WIKI_URL.length + 1);
      else if (/^[a-z][a-z0-9+.-]*:|^\/\//i.test(target)) continue;
      const [path, fragment = ""] = target.split("#");
      const decoded = decodeURIComponent(path).replace(/\?.*$/, "");
      const page = decoded ? posix.normalize(decoded).replace(/\.md$/, "") + ".md" : name;
      if (files.has(decoded) && !decoded.endsWith(".md")) continue;
      if (!pages.has(page)) throw new Error(`${name}: missing wiki page or asset ${target}`);
      if (fragment && !pages.get(page).headings.has(decodeURIComponent(fragment))) {
        throw new Error(`${name}: missing wiki heading ${target}`);
      }
    }
  }
  return pages.size;
}

module.exports = { isWikiFile, readDirectory, validateWiki };
