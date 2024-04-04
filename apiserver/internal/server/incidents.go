package server

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/metoro-io/statusphere/common/api"
	"github.com/patrickmn/go-cache"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"net/http"
	"sort"
	"strconv"
	"strings"
)

type IncidentsResponse struct {
	Incidents []api.Incident `json:"incidents"`
	IsIndexed bool           `json:"isIndexed"`
}

// incidents is a handler for the /incidents endpoint.
// It has a required query parameter of statusPageUrl
// It has an optional query parameter of impact (default is all), which is an array of impacts e.g. impact=critical,major,minor,none to exclude maintenance
func (s *Server) incidents(context *gin.Context) {
	ctx := context.Request.Context()
	statusPageUrl := context.Query("statusPageUrl")
	if statusPageUrl == "" {
		context.JSON(http.StatusBadRequest, gin.H{"error": "statusPageUrl is required"})
		return
	}

	impactQuery := context.Query("impact")
	var impacts []api.Impact
	if impactQuery != "" {
		impactsStr := strings.Split(impactQuery, ",")
		for _, impactStr := range impactsStr {
			impact, err := api.ParseImpact(impactStr)
			if err != nil {
				context.JSON(http.StatusBadRequest, gin.H{"error": "invalid impact"})
				return
			}
			impacts = append(impacts, impact)
		}
	}

	var limit *int = nil
	if limitStr := context.Query("limit"); limitStr != "" {
		limitInt, err := strconv.Atoi(limitStr)
		if err != nil {
			context.JSON(http.StatusBadRequest, gin.H{"error": "limit must be an integer"})
			return
		}
		limit = &limitInt
	}

	// Check to see that the status page is known to statusphere and is indexed
	statusPage, found := s.statusPageCache.Get(statusPageUrl)
	if !found {
		context.JSON(http.StatusNotFound, gin.H{"error": "status page not known to statusphere"})
		return
	}

	statusPageCasted, ok := statusPage.(api.StatusPage)
	if !ok {
		context.JSON(http.StatusInternalServerError, gin.H{"error": "failed to cast status page"})
		return
	}

	if !statusPageCasted.IsIndexed {
		context.JSON(http.StatusOK, IncidentsResponse{Incidents: []api.Incident{}, IsIndexed: false})
		return
	}

	// Attempt to get the incidents from the cache
	incidents, found, err := s.getIncidentsFromCache(ctx, statusPageUrl, impacts)
	if err != nil {
		s.logger.Error("failed to get incidents from cache", zap.Error(err))
		context.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get incidents from cache"})
		return
	}
	if found {
		sortIncidentsDescending(incidents)
		if limit != nil && len(incidents) > *limit {
			incidents = incidents[:*limit]
		}
		context.JSON(http.StatusOK, IncidentsResponse{Incidents: incidents, IsIndexed: true})
		return
	}

	// Attempt to get the incidents from the database
	incidents, found, err = s.getIncidentsFromDatabase(ctx, statusPageUrl)
	if err != nil {
		s.logger.Error("failed to get incidents from database", zap.Error(err))
		context.JSON(http.StatusInternalServerError, gin.H{"error": "failed to get incidents from database"})
		return
	}
	if !found {
		context.JSON(http.StatusNotFound, gin.H{"error": "status page not known to statusphere"})
		return
	}

	sortIncidentsDescending(incidents)
	s.incidentCache.Set(statusPageUrl, incidents, cache.DefaultExpiration)
	if limit != nil && len(incidents) > *limit {
		incidents = incidents[:*limit]
	}
	context.JSON(http.StatusOK, IncidentsResponse{Incidents: incidents, IsIndexed: true})
}

func sortIncidentsDescending(incidents []api.Incident) {
	sort.Slice(incidents, func(i, j int) bool {
		return incidents[i].StartTime.After(incidents[j].StartTime)
	})
}

// getIncidentsFromCache attempts to get the incidents from the cache.
// If the incidents are found in the cache, it returns them.
// If the incidents are not found in the cache, it returns false for the second return value.
func (s *Server) getIncidentsFromCache(ctx context.Context, statusPageUrl string, impacts []api.Impact) ([]api.Incident, bool, error) {
	incidents, found := s.incidentCache.Get(statusPageUrl)
	if !found {
		return nil, false, nil
	}

	incidentsCasted, ok := incidents.([]api.Incident)
	if !ok {
		return nil, false, errors.New("failed to cast incidents to []api.Incident")
	}

	if len(impacts) > 0 {
		var filteredIncidents []api.Incident
		for _, incident := range incidentsCasted {
			for _, impact := range impacts {
				if incident.Impact == impact {
					filteredIncidents = append(filteredIncidents, incident)
				}
			}
		}
		incidentsCasted = filteredIncidents
	}

	return incidentsCasted, true, nil
}

// getIncidentsFromDatabase attempts to get the incidents from the database.
// If the incidents are found in the database, it returns them.
// If the incidents are not found in the database, it returns false for the second return value.
func (s *Server) getIncidentsFromDatabase(ctx context.Context, statusPageUrl string) ([]api.Incident, bool, error) {
	incidents, err := s.dbClient.GetIncidents(ctx, statusPageUrl)
	if err != nil {
		return nil, false, err
	}

	if len(incidents) == 0 {
		// See if the status page exists
		statusPage, err := s.dbClient.GetStatusPage(ctx, statusPageUrl)
		if err != nil {
			return nil, false, err
		}
		if statusPage == nil {
			// The status page does not exist
			return nil, false, nil
		}
	}

	return incidents, true, nil
}
