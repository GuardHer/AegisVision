type Result = {
  success: boolean;
  data: Array<any>;
};

export const getAsyncRoutes = (): Promise<Result> => {
  return Promise.resolve({ success: true, data: [] });
};
