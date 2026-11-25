package match

import (
	"strings"

	"github.com/MIERAI/docs-platform/internal/model"
)

func (ix *index) envVar(r model.Reference) (Status, model.Confidence) {
	if ambientEnv[r.Text] {
		return External, r.Confidence
	}
	if ix.has(model.KindEnvVar, r.Text) || ix.has(model.KindConfigKey, r.Text) ||
		ix.has(model.KindWorkflowInput, r.Text) || ix.inCode(r.Text) {
		return Resolved, r.Confidence
	}
	return Unresolved, r.Confidence
}

func (ix *index) flag(r model.Reference) (Status, model.Confidence) {
	key := norm(model.KindCLIFlag, r.Text)
	switch {
	case r.Program != "" && !ix.programs[r.Program]:
		return External, r.Confidence // another tool's flag
	case r.Program == "" && !ix.hasFlags:
		return External, r.Confidence // this repository defines no flags: nothing to check against
	}
	if ix.flagsByProg[r.Program][key] || ix.flagsByProg[""][key] || (r.Program == "" && ix.has(model.KindCLIFlag, r.Text)) {
		return Resolved, r.Confidence
	}
	if ix.inCode(r.Text, "-"+key, "--"+key) || (ix.inCode(key) && len(key) > 3 && strings.Contains(key, "-")) {
		return Resolved, r.Confidence
	}
	if r.Program == "" {
		// a bare flag could belong to any tool; only a near-miss of one of ours is likely drift
		if ix.nearest(model.KindCLIFlag, r.Text) != "" {
			return Unresolved, min(r.Confidence, model.ConfMedium)
		}
		return Unresolved, model.ConfLow
	}
	return Unresolved, r.Confidence
}

func (ix *index) command(r model.Reference) (Status, model.Confidence) {
	if !ix.programs[r.Program] || r.Program == "make" {
		return External, r.Confidence
	}
	own, shared := ix.cmdsByProg[r.Program], ix.cmdsByProg[""]
	if len(own) == 0 && len(shared) == 0 {
		return External, r.Confidence // the program takes no sub-commands we know of
	}
	if own[r.Text] || shared[r.Text] {
		return Resolved, r.Confidence
	}
	if len(own) == 0 {
		return Unresolved, model.ConfLow // only commands of unknown programs exist
	}
	return Unresolved, r.Confidence
}

func (ix *index) makeTarget(r model.Reference) (Status, model.Confidence) {
	if ix.has(model.KindMakeTarget, r.Text) {
		return Resolved, r.Confidence
	}
	if isPlaceholderPath(r.Text) {
		return External, r.Confidence // an example such as "make <target>" written as "make x"
	}
	if !ix.hasMake {
		return Unresolved, model.ConfMedium // the docs run make but the repository has no Makefile
	}
	return Unresolved, r.Confidence
}

func (ix *index) metric(r model.Reference) (Status, model.Confidence) {
	if ix.has(model.KindMetric, r.Text) || ix.inCode(r.Text, metricBase(r.Text)) {
		return Resolved, r.Confidence
	}
	if ix.metricNS[metricNamespace(metricBase(r.Text))] {
		return Unresolved, max(r.Confidence, model.ConfMedium) // our namespace, but no such metric
	}
	return External, r.Confidence // probably an exporter's metric (node_, kube_, container_ ...)
}

func (ix *index) configKey(r model.Reference) (Status, model.Confidence) {
	if ix.inCode(r.Text) {
		return Resolved, r.Confidence
	}
	for _, p := range ix.configPaths {
		if p == r.Text || strings.HasSuffix(p, "."+r.Text) {
			return Resolved, r.Confidence
		}
	}
	return Unresolved, model.ConfLow
}
