import type { SidebarsConfig } from "@docusaurus/plugin-content-docs";

const sidebar: SidebarsConfig = {
  apisidebar: [
    {
      type: "doc",
      id: "api-reference/rest/dock-scheduling-api",
    },
    {
      type: "category",
      label: "Appointments",
      link: {
        type: "doc",
        id: "api-reference/rest/appointments",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/book-appointment",
          label: "Book an appointment",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/list-appointments",
          label: "List appointments",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/get-appointment",
          label: "Get an appointment",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/reschedule-appointment",
          label: "Reschedule a BOOKED appointment",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/cancel-appointment",
          label: "Cancel an appointment",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/report-eta",
          label: "Report an ETA",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Doors",
      link: {
        type: "doc",
        id: "api-reference/rest/doors",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-door-availability",
          label: "Free windows of a door",
          className: "api-method get",
        },
      ],
    },
    {
      type: "category",
      label: "Dock calendar",
      link: {
        type: "doc",
        id: "api-reference/rest/dock-calendar",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/get-dock-calendar",
          label: "Get a site's dock calendar",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/declare-operating-hours",
          label: "Declare the operating hours of a site",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/declare-calendar-settings",
          label: "Declare the booking settings of a site",
          className: "api-method put",
        },
        {
          type: "doc",
          id: "api-reference/rest/schedule-door-closure",
          label: "Schedule a door closure",
          className: "api-method post",
        },
        {
          type: "doc",
          id: "api-reference/rest/lift-door-closure",
          label: "Lift a door closure",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Sweeps",
      link: {
        type: "doc",
        id: "api-reference/rest/sweeps",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/run-no-show-sweep",
          label: "Run the no-show sweep",
          className: "api-method post",
        },
      ],
    },
    {
      type: "category",
      label: "Health",
      link: {
        type: "doc",
        id: "api-reference/rest/health",
      },
      items: [
        {
          type: "doc",
          id: "api-reference/rest/healthz",
          label: "Liveness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/readyz",
          label: "Readiness probe",
          className: "api-method get",
        },
        {
          type: "doc",
          id: "api-reference/rest/metrics",
          label: "Prometheus metrics",
          className: "api-method get",
        },
      ],
    },
  ],
};

export default sidebar.apisidebar;
