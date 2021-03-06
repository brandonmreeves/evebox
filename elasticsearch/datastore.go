/* Copyright (c) 2016-2017 Jason Ish
 * All rights reserved.
 *
 * Redistribution and use in source and binary forms, with or without
 * modification, are permitted provided that the following conditions
 * are met:
 *
 * 1. Redistributions of source code must retain the above copyright
 *    notice, this list of conditions and the following disclaimer.
 * 2. Redistributions in binary form must reproduce the above copyright
 *    notice, this list of conditions and the following disclaimer in the
 *    documentation and/or other materials provided with the distribution.
 *
 * THIS SOFTWARE IS PROVIDED ``AS IS'' AND ANY EXPRESS OR IMPLIED
 * WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE IMPLIED WARRANTIES OF
 * MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
 * DISCLAIMED. IN NO EVENT SHALL THE AUTHOR BE LIABLE FOR ANY DIRECT,
 * INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES
 * (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
 * SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION)
 * HOWEVER CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT,
 * STRICT LIABILITY, OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING
 * IN ANY WAY OUT OF THE USE OF THIS SOFTWARE, EVEN IF ADVISED OF THE
 * POSSIBILITY OF SUCH DAMAGE.
 */

package elasticsearch

import (
	"fmt"
	"github.com/jasonish/evebox/core"
	"github.com/jasonish/evebox/eve"
	"github.com/jasonish/evebox/log"
	"github.com/jasonish/evebox/util"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"time"
)

type DataStore struct {
	es *ElasticSearch
	core.UnimplementedDatastore
}

func NewDataStore(es *ElasticSearch) (*DataStore, error) {
	datastore := DataStore{
		es: es,
	}
	return &datastore, nil
}

func (d *DataStore) GetEveEventSink() core.EveEventSink {
	return NewIndexer(d.es)
}

func (s *DataStore) asKeyword(keyword string) string {
	return fmt.Sprintf("%s.%s", keyword, s.es.keyword)
}

// FindFlow finds the flow events matching the query parameters in options.
func (d *DataStore) FindFlow(flowId uint64, proto string, timestamp string,
	srcIp string, destIp string) (interface{}, error) {

	query := NewEventQuery()
	query.Size = 1

	query.EventType("flow")
	query.AddFilter(TermQuery("flow_id", flowId))
	query.AddFilter(TermQuery("proto", proto))
	query.AddFilter(RangeLte("flow.start", timestamp))
	query.AddFilter(RangeGte("flow.end", timestamp))
	query.ShouldHaveIp(srcIp, d.es.keyword)
	query.ShouldHaveIp(destIp, d.es.keyword)

	response, err := d.es.Search(query)
	if err != nil {
		log.Error("%v", err)
		return nil, err
	}

	return response.Hits.Hits, nil
}

// FindNetflow finds netflow events matching the parameters in options.
func (s *DataStore) FindNetflow(options core.EventQueryOptions, sortBy string,
	order string) (interface{}, error) {

	size := int64(10)

	if options.Size > 0 {
		size = options.Size
	}

	if order == "" {
		order = "desc"
	}

	query := NewEventQuery()
	query.AddFilter(TermQuery("event_type", "netflow"))

	if options.TimeRange != "" {
		query.AddTimeRangeFilter(options.TimeRange)
	}

	if options.QueryString != "" {
		query.AddFilter(QueryString(options.QueryString))
	}

	if sortBy != "" {
		query.Aggs["agg"] = TopHitsAgg(sortBy, order, size)
	} else {
		query.Size = size
	}

	response, err := s.es.Search(query)
	if err != nil {
		return nil, err
	}

	// Unwrap response.
	hits := response.Aggregations.GetMap("agg").GetMap("hits").Get("hits")

	return map[string]interface{}{
		"data": hits,
	}, nil
}

// AddTagsToAlertGroup adds the provided tags to all alerts that match the
// provided alert group parameters.
func (s *DataStore) AddTagsToAlertGroup(p core.AlertGroupQueryParams, tags []string) error {

	mustNot := []interface{}{}
	for _, tag := range tags {
		mustNot = append(mustNot, TermQuery("tags", tag))
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"filter": []interface{}{
					ExistsQuery("event_type"),
					KeywordTermQuery("event_type", "alert", s.es.keyword),
					RangeQuery{
						Field: "@timestamp",
						Gte:   eve.FormatTimestampUTC(p.MinTimestamp),
						Lte:   eve.FormatTimestampUTC(p.MaxTimestamp),
					},
					KeywordTermQuery("src_ip", p.SrcIP, s.es.keyword),
					KeywordTermQuery("dest_ip", p.DstIP, s.es.keyword),
					TermQuery("alert.signature_id", p.SignatureID),
				},
				"must_not": mustNot,
			},
		},
		"_source": "tags",
		"sort": []interface{}{
			"_doc",
		},
		"size": 10000,
	}

	searchResponse, err := s.es.SearchScroll(query, "1m")
	if err != nil {
		log.Error("Failed to initialize scroll: %v", err)
		return err
	}
	scrollID := searchResponse.ScrollId
	defer func() {
		response, err := s.es.DeleteScroll(scrollID)
		if err != nil {
			log.Error("Failed to delete scroll id: %v", err)
		}
		io.Copy(ioutil.Discard, response.Body)
	}()

	for {
		log.Debug("Search response total: %d; hits: %d",
			searchResponse.Hits.Total, len(searchResponse.Hits.Hits))

		if len(searchResponse.Hits.Hits) == 0 {
			break
		}

		// We do this in a retry loop as some documents may fail to be
		// updated. Most likely rejected due to max thread count or
		// something.
		maxRetries := 5
		retries := 0
		for {
			retry, err := BulkUpdateTags(s.es, searchResponse.Hits.Hits,
				tags, nil)
			if err != nil {
				log.Error("BulkAddTags failed: %v", err)
				return err
			}
			if !retry {
				break
			}
			retries++
			if retries > maxRetries {
				log.Warning("Errors occurred archive events, not all events may have been archived.")
				break
			}
		}

		// Get next set of events to archive.
		searchResponse, err = s.es.Scroll(scrollID, "1m")
		if err != nil {
			log.Error("Failed to fetch from scroll: %v", err)
			return err
		}

	}

	s.es.Refresh()

	return nil
}

const ACTION_ARCHIVED = "archived"

const ACTION_ESCALATED = "escalated"

const ACTION_DEESCALATED = "de-escalated"

const ACTION_COMMENT = "comment"

type HistoryEntry struct {
	Timestamp string `json:"timestamp"`
	Username  string `json:"username"`
	Action    string `json:"action"`
	Comment   string `json:"comment,omitempty"`
}

func (s *DataStore) buildAlertGroupQuery(p core.AlertGroupQueryParams) *EventQuery {
	q := EventQuery{}
	q.AddFilter(ExistsQuery("event_type"))
	q.AddFilter(KeywordTermQuery("event_type", "alert", s.es.keyword))
	q.AddFilter(RangeQuery{
		Field: "@timestamp",
		Gte:   eve.FormatTimestampUTC(p.MinTimestamp),
		Lte:   eve.FormatTimestampUTC(p.MaxTimestamp),
	})
	q.AddFilter(KeywordTermQuery("src_ip", p.SrcIP, s.es.keyword))
	q.AddFilter(KeywordTermQuery("dest_ip", p.DstIP, s.es.keyword))
	q.AddFilter(TermQuery("alert.signature_id", p.SignatureID))
	return &q
}

// ArchiveAlertGroupByQuery uses the Elastic Search update_by_query API to
// archive events with a query instead of updating each document. This is
// only available in Elastic Search v5+.
func (s *DataStore) AddTagsToAlertGroupsByQuery(p core.AlertGroupQueryParams, tags []string, action HistoryEntry) error {
	log.Println("AddTagsToAlertGroupsByQuery")
	mustNot := []interface{}{}
	for _, tag := range tags {
		mustNot = append(mustNot, TermQuery("tags", tag))
	}

	query := s.buildAlertGroupQuery(p)
	if len(mustNot) > 0 {
		query.Query.Bool.MustNot = mustNot
	}
	query.Script = &Script{
		Lang: "painless",
		Inline: `
		        if (params.tags != null) {
			        if (ctx._source.tags == null) {
			            ctx._source.tags = new ArrayList();
			        }
			        for (tag in params.tags) {
			            if (!ctx._source.tags.contains(tag)) {
			                ctx._source.tags.add(tag);
			            }
			        }
			    }
			    if (ctx._source.evebox == null) {
			        ctx._source.evebox = new HashMap();
			    }
			    if (ctx._source.evebox.history == null) {
			        ctx._source.evebox.history = new ArrayList();
			    }
			    ctx._source.evebox.history.add(params.action);
		`,
		Params: map[string]interface{}{
			"tags":   tags,
			"action": action,
		},
	}

	response, err := s.es.doUpdateByQuery(query)
	if err != nil {
		log.Error("failed to update by query: %v", err)
		return err
	}
	log.Info("Events updated: %v; failures=%d",
		response.Get("updated"), len(response.GetMapList("failures")))

	return nil
}

// ArchiveAlertGroup is a specialization of AddTagsToAlertGroup.
func (s *DataStore) ArchiveAlertGroup(p core.AlertGroupQueryParams, user core.User) error {
	tags := []string{"archived", "evebox.archived"}
	if s.es.MajorVersion < 5 {
		return s.AddTagsToAlertGroup(p, tags)
	}
	return s.AddTagsToAlertGroupsByQuery(p, tags, HistoryEntry{
		Action:    ACTION_ARCHIVED,
		Timestamp: FormatTimestampUTC(time.Now()),
		Username:  user.Username,
	})
}

// EscalateAlertGroup is a specialization of AddTagsToAlertGroup.
func (s *DataStore) EscalateAlertGroup(p core.AlertGroupQueryParams, user core.User) error {
	tags := []string{"escalated", "evebox.escalated"}
	if s.es.MajorVersion < 5 {
		return s.AddTagsToAlertGroup(p, tags)
	}
	history := HistoryEntry{
		Username:  user.Username,
		Action:    ACTION_ESCALATED,
		Timestamp: FormatTimestampUTC(time.Now()),
	}
	return s.AddTagsToAlertGroupsByQuery(p, tags, history)
}

func (s *DataStore) DeEscalateAlertGroup(p core.AlertGroupQueryParams, user core.User) error {
	tags := []string{"escalated", "evebox.escalated"}
	if s.es.MajorVersion < 5 {
		return s.RemoveTagsFromAlertGroup(p, tags)
	}
	return s.RemoveTagsFromAlertGroupsByQuery(p, tags, HistoryEntry{
		Username:  user.Username,
		Timestamp: FormatTimestampUTC(time.Now()),
		Action:    ACTION_DEESCALATED,
	})
}

func (s *DataStore) RemoveTagsFromAlertGroupsByQuery(p core.AlertGroupQueryParams,
	tags []string, action HistoryEntry) error {
	should := []interface{}{}
	for _, tag := range tags {
		should = append(should, TermQuery("tags", tag))
	}

	query := s.buildAlertGroupQuery(p)
	query.Query.Bool.Should = should
	query.Script = &Script{
		Lang: "painless",
		Inline: `
			    if (ctx._source.tags != null) {
			        for (tag in params.tags) {
			            ctx._source.tags.removeIf(entry -> entry == tag);
			        }
			    }
			    if (ctx._source.evebox == null) {
			        ctx._source.evebox = new HashMap();
			    }
			    if (ctx._source.evebox.history == null) {
			        ctx._source.evebox.history = new ArrayList();
			    }
			    ctx._source.evebox.history.add(params.action);
		`,
		Params: map[string]interface{}{
			"tags":   tags,
			"action": action,
		},
	}

	response, err := s.es.doUpdateByQuery(query)
	if err != nil {
		log.Error("failed to update by query: %v", err)
		return err
	}
	log.Info("Events updated: %v; failures=%d",
		response.Get("updated"), len(response.GetMapList("failures")))

	return nil
}

// RemoveTagsFromAlertGroup removes the given tags from all alerts matching
// the provided parameters.
func (s *DataStore) RemoveTagsFromAlertGroup(p core.AlertGroupQueryParams, tags []string) error {

	filter := []interface{}{
		ExistsQuery("event_type"),
		KeywordTermQuery("event_type", "alert", s.es.keyword),
		RangeQuery{
			Field: "@timestamp",
			Gte:   eve.FormatTimestampUTC(p.MinTimestamp),
			Lte:   eve.FormatTimestampUTC(p.MaxTimestamp),
		},
		KeywordTermQuery("src_ip", p.SrcIP, s.es.keyword),
		KeywordTermQuery("dest_ip", p.DstIP, s.es.keyword),
		TermQuery("alert.signature_id", p.SignatureID),
	}

	for _, tag := range tags {
		filter = append(filter, TermQuery("tags", tag))
	}

	query := map[string]interface{}{
		"query": map[string]interface{}{
			"bool": map[string]interface{}{
				"filter": filter,
			},
		},
		"_source": "tags",
		"sort": []interface{}{
			"_doc",
		},
		"size": 10000,
	}

	searchResponse, err := s.es.SearchScroll(query, "1m")
	if err != nil {
		log.Error("Failed to initialize scroll: %v", err)
		return err
	}
	scrollID := searchResponse.ScrollId
	defer func() {
		response, err := s.es.DeleteScroll(scrollID)
		if err != nil {
			log.Error("Failed to delete scroll id: %v", err)
		}
		io.Copy(ioutil.Discard, response.Body)
	}()

	for {
		log.Debug("Search response total: %d; hits: %d",
			searchResponse.Hits.Total, len(searchResponse.Hits.Hits))

		if len(searchResponse.Hits.Hits) == 0 {
			break
		}

		// We do this in a retry loop as some documents may fail to be
		// updated. Most likely rejected due to max thread count or
		// something.
		maxRetries := 5
		retries := 0
		for {
			retry, err := BulkUpdateTags(s.es, searchResponse.Hits.Hits,
				nil, tags)
			if err != nil {
				log.Error("BulkAddTags failed: %v", err)
				return err
			}
			if !retry {
				break
			}
			retries++
			if retries > maxRetries {
				log.Warning("Errors occurred archive events, not all events may have been archived.")
				break
			}
		}

		// Get next set of events to archive.
		searchResponse, err = s.es.Scroll(scrollID, "1m")
		if err != nil {
			log.Error("Failed to fetch from scroll: %v", err)
			return err
		}

	}

	s.es.Refresh()

	return nil
}

func (s *DataStore) CommentOnAlertGroup(p core.AlertGroupQueryParams, user core.User, comment string) error {
	history := HistoryEntry{
		Username:  user.Username,
		Action:    ACTION_COMMENT,
		Comment:   comment,
		Timestamp: FormatTimestampUTC(time.Now()),
	}
	return s.AddTagsToAlertGroupsByQuery(p, nil, history)
}

func (s *DataStore) CommentOnEventId(eventId string, user core.User, comment string) error {

	event, err := s.GetEventById(eventId)
	if err != nil {
		return errors.Wrapf(err, "failed to find event with ID %s", eventId)
	}
	doc := Document{event}

	action := HistoryEntry{
		Username:  user.Username,
		Action:    ACTION_COMMENT,
		Comment:   comment,
		Timestamp: FormatTimestampUTC(time.Now()),
	}

	query := EventQuery{}
	query.Script = &Script{
		Lang: "painless",
		Inline: `
			    if (ctx._source.evebox == null) {
			        ctx._source.evebox = new HashMap();
			    }
			    if (ctx._source.evebox.history == null) {
			        ctx._source.evebox.history = new ArrayList();
			    }
			    ctx._source.evebox.history.add(params.action);
		`,
		Params: map[string]interface{}{
			"action": action,
		},
	}

	//query := map[string]interface{}{
	//	"script": &Script{
	//		Lang: "painless",
	//		Inline: `
	//		    if (ctx._source.evebox == null) {
	//		        ctx._source.evebox = new HashMap();
	//		    }
	//		    if (ctx._source.evebox.history == null) {
	//		        ctx._source.evebox.history = new ArrayList();
	//		    }
	//		    ctx._source.evebox.history.add(params.action);
	//	`,
	//		Params: map[string]interface{}{
	//			"action": action,
	//		},
	//	},
	//}

	log.Println(util.ToJson(query))

	_, err = s.es.Update(doc.Index(), doc.Type(), doc.Id(), query)
	if err != nil {
		log.Error("error: %v", err)
	}

	return errors.New("CommentOnEventId not implemented by Elastic Search datastore.")
}
