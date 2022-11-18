/* {"URI": "basic/events-explicit", "Method": "POST", "ReturnBody": true } */

SELECT EVENTS.*
FROM (SELECT ID, QUANTITY FROM EVENTS) EVENTS
JOIN (SELECT * FROM EVENTS_PERFORMANCE) EVENTS_PERFORMANCE ON EVENTS.ID = EVENTS_PERFORMANCE.EVENT_ID